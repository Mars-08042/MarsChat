# 系统架构（当前状态）

> 本文描述**当前代码**的实际结构，随重构步骤更新，最近一次更新于步骤 1（2026-11-18）。目标架构与选型理由见 [ADR-0005](adr/0005-distributed-architecture.md)，进度见 [后端重构计划](../plans/active/backend-refactor.md)。旧的 Gin 单体结构已随步骤 0 删除，需要时查看提交 `6624cea`。

## 1. 部署形态

- 后端按 ADR-0005 拆分为 access、account、social、message、delivery、media 六个服务。步骤 1 完成时只有 account、social 两个**空服务骨架**：能启动、连接各自的数据库和 Redis、注册到 Consul，暂未提供业务接口。
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
  internal/biz/          业务逻辑（步骤 2 起）
  internal/service/      实现 proto 定义的接口（步骤 2 起）
api/<服务>/v1/           对外接口契约 *.proto（步骤 2 起）
pkg/conf/                跨服务共用的配置消息（conf.proto）
pkg/bootstrap/           启动公共组件：配置、日志、注册中心、MySQL、Redis、健康检查
pkg/balancer/            设置 Kratos gRPC 负载均衡的全局选择器（修复 v3.0.0 的初始化顺序问题）
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

1. 读取 `-conf` 指定的配置目录（默认 `app/account/configs`），解析到 `conf.Bootstrap`。
2. 按 `log` 配置创建 `slog` 日志器，设为 Kratos 默认日志器。
3. `wireApp` 依次创建：Consul 注册中心、gRPC 服务器、MySQL 连接池（`gorm.Open` 立即 ping）、Redis 客户端（ping 一次）、HTTP 服务器。任一依赖不可用时启动失败并退出，退出码为 1。
4. `app.Run()` 启动服务器并注册到 Consul；收到 SIGINT/SIGTERM 后先从 Consul 注销，再停止服务器，最后按创建的逆序关闭 Redis、MySQL。

## 5. 横切约定

### 5.1 配置

- 配置结构用 protobuf 定义：共用片段在 [pkg/conf/conf.proto](../../pkg/conf/conf.proto)（`Server`、`Registry`、`MySQL`、`Redis`、`Log`），每个服务的 `internal/conf/conf.proto` 用它们组合出自己的 `Bootstrap`。新增服务专属配置时在服务自己的 proto 中定义。
- 配置文件中的 `${XXX}` 引用环境变量 `MARSCHAT_XXX`，`${XXX:默认值}` 在变量缺失时取默认值。默认值对应本地开发环境，密码只能来自环境变量。引用的变量缺失且没有默认值时会替换成空字符串，不会报错，密码缺失会在连接依赖时失败。
- 时长字段按 protobuf `Duration` 的 JSON 格式书写，单位只能是秒（`0.5s`、`3600s`），写成 `500ms`、`1h` 会导致启动失败。每个服务都有解析真实配置文件的单元测试。
- 配置只在启动时加载一次，不支持热更新。

### 5.2 日志

- 统一用标准库 `log/slog`，由 `bootstrap.NewLogger` 创建，输出到 stdout，格式 `text` 或 `json`，每条日志带 `service.name`、`service.version`。
- GORM 日志接入同一个日志器（`component=gorm`），只记录慢查询（超过 `mysql.slow_threshold`）和错误，查不到记录不算错误；SQL 只打印占位符，不打印参数。

### 5.3 服务注册与发现

- 注册中心是 Consul，服务名为 `marschat-<服务>`。实例 ID 为 `<服务名>-<主机名>-<进程号>`：Consul 要求同一 agent 上的实例 ID 唯一，本地多个服务共用一台主机，只用主机名会互相覆盖。
- 实例注册时登记 gRPC 和 HTTP 两个端点，带 TTL 心跳检查（每 10 秒上报，20 秒未上报变为 critical，critical 持续 10 分钟后注销）。发现方只拿到检查通过的实例。
- `registry.consul.health_check` 控制是否让 Consul 主动 TCP 探测服务端口。本地开发时为 `false`：服务监听宿主机 `127.0.0.1`，容器里的 Consul 探测不到。服务容器化（步骤 5）后开启。
- 服务间调用用 Kratos gRPC 客户端，地址写成 `discovery:///marschat-<服务>`，负载均衡为加权轮询（wrr）。
- Kratos v3.0.0 的 gRPC 负载均衡器在包初始化时就读取了全局选择器，而默认选择器设置得更晚，经服务发现调用会空指针 panic。`pkg/balancer` 在更早的初始化阶段设置选择器，`pkg/bootstrap` 匿名导入了它；不使用 bootstrap 的程序（如 e2e 测试）要自己匿名导入 `pkg/balancer`。原因与验证见 [lessons/kratos-grpc-selector-nil.md](../lessons/kratos-grpc-selector-nil.md)。

### 5.4 健康检查

| 探针 | 位置 | 行为 |
|---|---|---|
| 存活 | HTTP `GET /healthz` | 进程能处理请求就返回 200，不检查依赖，依赖故障时重启进程没有意义 |
| 就绪 | HTTP `GET /readyz` | 检查 MySQL、Redis，全部可用返回 200，否则返回 503 和失败的依赖名；错误详情只写日志 |
| gRPC | `grpc.health.v1.Health` | Kratos 默认注册，服务启动后为 SERVING，停止时为 NOT_SERVING |

### 5.5 代码生成

| 内容 | 命令 | 说明 |
|---|---|---|
| 配置 proto | `make conf` | `buf generate --template buf.gen.conf.yaml`，插件 `protoc-gen-go` 用 `go run` 固定版本 |
| 依赖注入 | `make wire` | `wire` v0.7.0 |
| 全部 | `make gen` | CI 会重新生成并检查与提交内容一致 |

`buf.yaml` 中 `pkg`、`app` 两个模块对应配置 proto，lint 使用 STANDARD 规则，例外是不要求版本后缀和“包名与目录一致”（配置 proto 不是对外契约，包名统一加 `marschat.` 前缀以免与依赖冲突）。接口 proto 的生成（gRPC、HTTP、OpenAPI）和参数校验在步骤 2 加入。

### 5.6 错误

业务错误用 Kratos errors，错误原因在各服务 proto 的 `ErrorReason` 枚举中定义，用 `errors.New(code, reason, message)` 或 `errors.NotFound(reason, message)` 等辅助函数手写构造，不使用 errors 代码生成器。具体错误码在步骤 2 随第一个业务接口确定。

### 5.7 测试

- 单元测试不依赖外部服务，`go test ./...` 即可运行。
- 依赖 compose 环境和已启动服务的测试放在 `test/e2e`，带 `e2e` 构建标签，用 `make e2e` 运行。

## 6. 关键技术组件

| 组件 | 版本 | 用途 |
|---|---|---|
| Go | 1.27.3 | 后端语言 |
| Kratos | v3.0.0；contrib/registry/consul v3 伪版本 `v3.0.0-20261026152050-dd0fb7df1e32` | 微服务框架、Consul 注册发现 |
| hashicorp/consul/api | v1.35.0 | Consul 客户端 |
| GORM + MySQL 驱动 | v1.31.2 / v1.6.0 | 数据访问 |
| go-redis | v9.23.0 | Redis 客户端 |
| wire | v0.7.0 | 编译期依赖注入 |
| buf | 1.71.0 | proto lint 与代码生成 |
| protoc-gen-go | v1.36.11 | 生成 proto 的 Go 代码 |

依赖组件的镜像版本见 [deployment.md](../deployment.md)。
