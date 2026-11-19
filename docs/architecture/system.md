# 系统架构（当前状态）

> 本文描述**当前代码**的实际结构，随重构步骤更新，最近一次更新于步骤 2（2026-11-18）。目标架构与选型理由见 [ADR-0005](adr/0005-distributed-architecture.md)，进度见 [后端重构计划](../plans/active/backend-refactor.md)。旧的 Gin 单体结构已随步骤 0 删除，需要时查看提交 `6624cea`。

## 1. 部署形态

- 后端按 ADR-0005 拆分为 access、account、social、message、delivery、media 六个服务。目前 account 已提供注册、登录、令牌、资料、设备与封禁接口（HTTP 与 gRPC），设计见 [account-service.md](../design/account-service.md)；social 仍是空骨架，只能启动、连接依赖并注册到 Consul。
- 开发阶段服务运行在宿主机，监听 `127.0.0.1`；MySQL、Redis、Kafka、Consul 由 [compose.yaml](../../compose.yaml) 提供。服务从步骤 5 起打包进容器。
- 端口规则、启动方式见 [deployment.md](../deployment.md)。

## 2. 仓库结构

单个 Go module（`github.com/Mars-08042/MarsChat`）的 monorepo：

```
app/<服务>/
  cmd/<服务>/            main.go 启动入口；wire.go 声明依赖，wire_gen.go 为生成代码
  configs/config.yaml    服务配置
  internal/conf/         Bootstrap 配置结构（conf.proto 及生成代码）
  internal/server/       gRPC、HTTP 服务器与路由注册
  internal/data/         存储客户端与 repo 实现
  internal/biz/          业务逻辑，定义 repo 接口
  internal/service/      实现 proto 定义的接口
  migrations/            版本化 SQL 迁移（golang-migrate 格式）
api/<服务>/v1/           接口契约 *.proto 及生成代码；api/openapi.yaml 为生成的 OpenAPI 文档
pkg/conf/                跨服务共用的配置消息（conf.proto）
pkg/bootstrap/           启动公共组件：配置加载与热更新、日志、注册中心、MySQL、Redis、健康检查
pkg/balancer/            设置 Kratos gRPC 负载均衡的全局选择器（修复 v3.0.0 的初始化顺序问题）
pkg/jsoncodec/           HTTP 的 json codec 按 protobuf JSON 规则处理 proto 消息（Kratos v3 默认不这样做）
pkg/middleware/accesslog/ 访问日志、panic 恢复与内部错误脱敏中间件
pkg/auth/                访问令牌签发（只有 account 使用）、校验中间件、吊销名单、当前用户
pkg/idgen/               snowflake ID，节点号用 Redis 租约分配
pkg/cache/               Cache-Aside 缓存组件
pkg/ratelimit/           Redis 滑动窗口限流
pkg/outbox/              事务 Outbox 的写入端
pkg/clientip/            按可信代理解析客户端 IP
tools/jwtkey/            生成开发用 JWT 密钥（make keys）
scripts/                 运维脚本（make grant-admin）
deploy/                  依赖组件的初始化脚本
test/e2e/                依赖 compose 环境的端到端测试（构建标签 e2e）
```

`internal` 保证服务之间不能互相 import 实现代码，跨服务只能通过 `api/` 的接口或事件通信。

## 3. 服务内分层

按 Kratos v3 布局，依赖方向为 `server → service → biz → data`：

| 层 | 职责 |
|---|---|
| server | 创建 gRPC、HTTP 服务器，挂中间件，注册 service 和探针 |
| service | 把 proto 请求转成 biz 调用，不写业务规则 |
| biz | 业务规则与用例，定义 repo 接口 |
| data | 实现 repo 接口，访问 MySQL、Redis、Kafka |

例外：HTTP 就绪探针需要检查存储连接，`server` 直接依赖 `data.Data` 的 `ReadinessChecks`，业务接口仍按上表分层。

各层通过 [wire](https://github.com/google/wire) 在编译期组装。每层提供 `ProviderSet`，`cmd/<服务>/wire.go` 声明组装关系，`make wire` 生成 `wire_gen.go`。

## 4. 启动流程

以 account 为例（[main.go](../../app/account/cmd/account/main.go)）：

1. 读取 `-conf` 指定的配置目录（默认 `app/account/configs`），解析到 `conf.Bootstrap` 并按 protovalidate 规则校验，不合法时启动失败。配置对象保持打开，用于热更新。
2. 按 `log` 配置创建 `slog` 日志器，设为 Kratos 默认日志器。
3. 用 `policy` 段创建 `biz.PolicyHolder`，并监听配置文件的 `policy` 段（见 5.1）。
4. `wireApp` 创建：Consul 注册中心、Redis 客户端（ping 一次）、MySQL 连接池（`gorm.Open` 立即 ping）、snowflake 生成器（抢占节点号租约）、各 repo 与用例、gRPC 与 HTTP 服务器。令牌密钥不合法、公钥与私钥不匹配、`revocation_ttl` 小于访问令牌有效期、固定验证码配在非 `log` 发送方上时都会启动失败。任一依赖不可用时退出，退出码为 1。用户 ID 布隆过滤器未就绪时在后台重建，不阻塞启动。
5. `app.Run()` 启动服务器并注册到 Consul；收到 SIGINT/SIGTERM 后先从 Consul 注销，再停止服务器，最后按创建的逆序释放节点号租约、关闭 MySQL、Redis。

## 5. 横切约定

### 5.1 配置

- 配置结构用 protobuf 定义：共用片段在 [pkg/conf/conf.proto](../../pkg/conf/conf.proto)（`Server`、`Registry`、`MySQL`、`Redis`、`Log`），每个服务的 `internal/conf/conf.proto` 用它们组合出自己的 `Bootstrap`。新增服务专属配置时在服务自己的 proto 中定义。
- 配置文件中的 `${XXX}` 引用环境变量 `MARSCHAT_XXX`，`${XXX:默认值}` 在变量缺失时取默认值。默认值对应本地开发环境，密码只能来自环境变量。引用的变量缺失且没有默认值时会替换成空字符串，不会报错，密码缺失会在连接依赖时失败。
- 时长字段按 protobuf `Duration` 的 JSON 格式书写，单位只能是秒（`0.5s`、`3600s`），写成 `500ms`、`1h` 会导致启动失败。每个服务都有解析真实配置文件的单元测试。
- 配置结构用 protovalidate 规则约束（例如限额必须大于 0），加载时校验。
- **热更新**：`bootstrap.WatchConfig` 监听某一段配置，文件变化后重新解析、校验，通过才原子替换，不通过时保留旧值并记录错误日志。目前只有 account 的 `policy` 段（短信与登录防护的限额）热更新，读取方每个请求取一次快照；其余配置修改后需要重启。

### 5.2 日志

- 统一用标准库 `log/slog`，由 `bootstrap.NewLogger` 创建，输出到 stdout，格式 `text` 或 `json`，每条日志带 `service.name`、`service.version`。
- GORM 日志接入同一个日志器（`component=gorm`），只记录慢查询（超过 `mysql.slow_threshold`）和错误，查不到记录不算错误；SQL 只打印占位符，不打印参数。
- 访问日志由 `pkg/middleware/accesslog` 输出（`msg="server request"`），只记操作名、状态码、原因和耗时，**不记录请求体**：登录、注册请求里有密码、验证码和刷新令牌。5xx 记 ERROR，其余记 INFO。不使用 Kratos 自带的 `logging` 和 `recovery` 中间件，它们会打印请求体，见 [lessons/kratos-v3-server-defaults.md](../lessons/kratos-v3-server-defaults.md)。
- 日志中的手机号脱敏为 `138****8000`。

### 5.3 服务注册与发现

- 注册中心是 Consul，服务名为 `marschat-<服务>`。实例 ID 为 `<服务名>-<主机名>-<进程号>`：Consul 要求同一 agent 上的实例 ID 唯一，本地多个服务共用一台主机，只用主机名会互相覆盖。
- 实例注册时登记 gRPC 和 HTTP 两个端点，带 TTL 心跳检查（每 10 秒上报，20 秒未上报变为 critical，critical 持续 10 分钟后注销）。发现方只拿到检查通过的实例。
- `registry.consul.health_check` 控制是否让 Consul 主动 TCP 探测服务端口。本地开发时为 `false`：服务监听宿主机 `127.0.0.1`，容器里的 Consul 探测不到。服务容器化（步骤 5）后开启。
- 服务间调用用 Kratos gRPC 客户端，地址写成 `discovery:///marschat-<服务>`，负载均衡为加权轮询（wrr）。
- Kratos v3.0.0 的 gRPC 负载均衡器在包初始化时就读取了全局选择器，而默认选择器设置得更晚，经服务发现调用会空指针 panic。`pkg/balancer` 在更早的初始化阶段设置选择器，`pkg/bootstrap` 匿名导入了它；不使用 bootstrap 的程序（如 e2e 测试）要自己匿名导入 `pkg/balancer`。原因与验证见 [lessons/kratos-grpc-selector-nil.md](../lessons/kratos-grpc-selector-nil.md)。

### 5.4 中间件与鉴权

HTTP 与 gRPC 共用同一条中间件链（[server.go](../../app/account/internal/server/server.go)），从外到内：

1. `accesslog`：访问日志、panic 恢复；非 Kratos 错误（数据库、Redis 等）记日志后换成 `500 INTERNAL`，不把内部信息返回给客户端。
2. 令牌校验 `pkg/auth.Server`，用 Kratos `selector` 只作用于需要登录的接口。公开接口用白名单列出，新增的接口默认需要登录。
3. 参数校验：Kratos validate contrib，按 proto 中的 protovalidate 规则校验，失败返回 `400 VALIDATOR`。先鉴权后校验，未登录的请求拿不到参数规则信息。

访问令牌：

- JWT，Ed25519（EdDSA）签名，有效期 15 分钟。account 持有私钥签发，其他服务只配置公钥（`auth` 配置段，各服务相同）。
- 校验：解析阶段只接受 EdDSA（防算法混淆），检查签发方、受众、过期时间和签发时间；再用一次 `MGET` 查 Redis 吊销名单（会话级、用户级），最后把 `auth.Principal`（用户 ID、会话 ID、角色、签发时间）放进 context，业务代码用 `auth.FromContext` 取。
- Redis 故障时吊销检查放行并记 WARN（开发者确认的取舍）。
- 刷新令牌、吊销名单的细节见 [account 设计 3.2](../design/account-service.md#32-令牌)，键见 [data-model.md](data-model.md#3-redis-键)。
- 内部接口（如 `AccountInternalService`）只注册在 gRPC 上，不经过令牌校验；服务网络不对外暴露，服务间认证（mTLS）为后续扩展。

客户端 IP：`pkg/clientip` 只在请求来自 `security.trusted_proxies` 中的地址时才读取 `X-Forwarded-For`，并取最右边一个不可信的地址；默认不信任任何代理，步骤 5 填入网关地址。

HTTP 编码：请求和响应按 protobuf JSON 规则（字段名 lowerCamelCase，枚举为名字字符串，int64 为字符串，Timestamp 为 RFC 3339，零值字段也输出）。Kratos v3 的 `json` codec 默认不这样做，由 `pkg/jsoncodec` 覆盖，`pkg/bootstrap` 已匿名导入，见 [lessons/kratos-v3-server-defaults.md](../lessons/kratos-v3-server-defaults.md)。客户端必须带 `Content-Type: application/json`，否则 Kratos 按表单解析。

### 5.5 健康检查

| 探针 | 位置 | 行为 |
|---|---|---|
| 存活 | HTTP `GET /healthz` | 进程能处理请求就返回 200，不检查依赖，依赖故障时重启进程没有意义 |
| 就绪 | HTTP `GET /readyz` | 检查 MySQL、Redis，全部可用返回 200，否则返回 503 和失败的依赖名；错误详情只写日志 |
| gRPC | `grpc.health.v1.Health` | Kratos 默认注册，服务启动后为 SERVING，停止时为 NOT_SERVING |

### 5.6 代码生成

| 内容 | 命令 | 说明 |
|---|---|---|
| 配置 proto | `make conf` | `buf generate --template buf.gen.conf.yaml`，插件 `protoc-gen-go` 用 `go run` 固定版本 |
| 接口 proto | `make api` | `buf generate --template buf.gen.yaml`：Go 消息、gRPC、Kratos HTTP 路由，以及合并的 `api/openapi.yaml`；插件同样用 `go run` 固定版本 |
| 依赖注入 | `make wire` | `wire` v0.7.0 |
| 全部 | `make gen` | CI 会重新生成并检查与提交内容一致 |

`buf.yaml` 有三个模块，lint 都使用 STANDARD 规则：

- `api`：接口契约。`error_reason.proto` 例外，不要求枚举值带类型前缀，让错误原因保持 `USER_NOT_FOUND` 这样的短名字。
- `pkg`、`app`：配置 proto，例外是不要求版本后缀和“包名与目录一致”（配置不是对外契约，包名统一加 `marschat.` 前缀以免与依赖冲突）。
- 依赖 `buf.build/googleapis/googleapis`（HTTP 注解）和 `buf.build/bufbuild/protovalidate`（校验规则），版本锁定在 `buf.lock`。
- 兼容性检查规则为 `FILE`：CI 中与基准分支（PR）或上一次推送比较，删除字段、修改字段号等破坏已生成代码的改动会失败。

### 5.7 错误

- 业务错误用 Kratos errors，错误原因在各服务 proto 的 `ErrorReason` 枚举中定义（account 见 [error_reason.proto](../../api/account/v1/error_reason.proto)），在 biz 层的 `errors.go` 中手写构造函数，不使用 errors 代码生成器。HTTP 状态码随错误类型确定：参数与验证码错误 400、凭据无效 401、封禁与无权限 403、不存在 404、冲突 409、限流 429、依赖故障 503。
- 错误体为 `{"code", "reason", "message", "metadata"}`。`message` 是给用户看的中文提示；客户端按 `reason` 分支处理，不解析 `message`。
- 限流类错误（429）在 `metadata.retry_after_seconds` 中给出需要等待的秒数。
- 框架产生的错误：令牌缺失、无效、过期或已吊销统一为 `401 UNAUTHORIZED`；参数不合法为 `400 VALIDATOR`；请求体无法解码为 `400 CODEC`；内部错误为 `500 INTERNAL`，不带细节。

### 5.8 测试

- 单元测试不依赖外部服务，`go test ./...` 即可运行。biz 层用内存假 repo；Redis 相关逻辑（Lua 脚本、限流、缓存、ID 租约、吊销名单）用 miniredis。
- 依赖 compose 环境和已启动服务的测试放在 `test/e2e`，带 `e2e` 构建标签，用 `make e2e` 运行；它会加载 `.env`，测试用其中的密码直接连 Redis、MySQL 做准备工作（清理本机 IP 的限流计数、设置管理员）。

## 6. 关键技术组件

| 组件 | 版本 | 用途 |
|---|---|---|
| Go | 1.27.3 | 后端语言 |
| Kratos | v3.0.0；contrib（registry/consul、middleware/jwt、middleware/validate）v3 伪版本 `v3.0.0-20261026152050-dd0fb7df1e32` | 微服务框架、Consul 注册发现、JWT 与参数校验中间件 |
| golang-jwt | v5.3.1 | JWT 解析与签名 |
| protovalidate | v1.2.0 | 接口参数与配置校验 |
| golang.org/x/crypto | v0.59.0 | bcrypt |
| golang.org/x/sync | v0.25.0 | singleflight |
| miniredis | v2.40.0 | 单元测试中的 Redis |
| hashicorp/consul/api | v1.35.0 | Consul 客户端 |
| GORM + MySQL 驱动 | v1.31.2 / v1.6.0 | 数据访问 |
| go-redis | v9.23.0 | Redis 客户端 |
| wire | v0.7.0 | 编译期依赖注入 |
| buf | 1.71.0 | proto lint 与代码生成 |
| protoc-gen-go / protoc-gen-go-grpc | v1.36.11 / v1.6.2 | 生成 proto 的 Go 代码与 gRPC 代码 |
| protoc-gen-go-http | Kratos v3 伪版本 `v3.0.0-20260626125723-668db92c2c00` | 生成 Kratos HTTP 路由 |
| protoc-gen-openapi | gnostic v0.7.1 | 生成 OpenAPI 文档 |

依赖组件的镜像版本见 [deployment.md](../deployment.md)。
