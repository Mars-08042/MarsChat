# 系统架构（当前状态）

> 本文描述**当前代码**的实际结构，最近一次更新于 2026-11-21（后端重构完成）。总览见 [ARCHITECTURE.md](../../ARCHITECTURE.md)，选型理由见 [ADR-0005](adr/0005-distributed-architecture.md)，重构过程与实测结果见 [后端重构计划](../plans/completed/backend-refactor.md)。

## 1. 部署形态

- 后端按 ADR-0005 拆分为 access、account、social、message、delivery、media 六个服务。目前 account 已提供注册、登录、令牌、资料、设备与封禁接口（HTTP 与 gRPC），设计见 [account-service.md](../design/account-service.md)；social 已提供好友、拉黑、群接口和给 message、delivery 用的内部 gRPC，设计见 [social-service.md](../design/social-service.md)；message、access、delivery 组成核心消息链路（会话、发送、推送、多端同步、补拉），设计见 [message-pipeline.md](../design/message-pipeline.md)，流转见 [data-flow.md](data-flow.md)；access 同时处理一对一通话信令（状态在 Redis，access 之间直连转发），设计见 [call-signaling.md](../design/call-signaling.md)；media 提供文件上传、下载地址和给 message 用的内部文件查询，文件存储可切换（默认仓库下的本地目录 `data/media/`，可选阿里云 OSS，[ADR-0007](adr/0007-object-storage.md)），设计见 [media-service.md](../design/media-service.md)。
- 服务有两种运行方式：日常开发运行在宿主机，监听 `127.0.0.1`；容器模式（compose profile `apps`）中 access、message、delivery 各两个实例（media 一个，本地存储挂载仓库的 `data/media`），客户端经 APISIX 网关（Standalone 模式，上游经 Consul 发现）访问。MySQL、Redis、Kafka、Consul 由 [compose.yaml](../../compose.yaml) 提供。设计见 [gateway-multi-instance.md](../design/gateway-multi-instance.md)。
- 可观测组件在 compose profile `monitoring` 中：Prometheus（经 Consul 发现抓取各实例）、Alertmanager、Grafana、Jaeger，只配合容器模式使用；压测工具 k6 在 profile `loadtest` 中按需运行。设计见 [observability.md](../design/observability.md)。
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
pkg/bootstrap/           启动公共组件：配置加载与热更新、日志、注册中心、MySQL、Redis、健康检查、服务端中间件链
pkg/balancer/            设置 Kratos gRPC 负载均衡的全局选择器（修复 v3.0.0 的初始化顺序问题）
pkg/jsoncodec/           HTTP 的 json codec 按 protobuf JSON 规则处理 proto 消息（Kratos v3 默认不这样做）
pkg/middleware/accesslog/ 访问日志、panic 恢复与内部错误脱敏中间件
pkg/middleware/retry/    gRPC 客户端重试中间件（幂等方法在连接级失败时重试一次）
pkg/auth/                访问令牌签发（只有 account 使用）、校验中间件、WebSocket 用的 Verifier、吊销名单、当前用户
pkg/idgen/               snowflake ID，节点号用 Redis 租约分配，租约丢失后换用新节点号
pkg/cache/               Cache-Aside 缓存组件，可选跨实例重建锁
pkg/bloom/               RedisBloom ID 布隆过滤器（就绪标记、脏标记、分布式重建）
pkg/ratelimit/           Redis 滑动窗口限流
pkg/outbox/              事务 Outbox：写入端与发布器（Redis 租约保证每个服务只有一个实例发布，Redis 发布订阅跨实例唤醒）
pkg/kafka/               Kafka 生产者、消费者（按事件类型分发、手动提交、原地重试、重试队列、死信队列）与记录头约定
pkg/route/               在线路由的 Redis 结构（access 写入，delivery 与 access 的通话信令读取）
pkg/accessclient/        按在线路由中的地址直连 access 实例的客户端缓存（不可达时摘除并冷却），delivery 与 access 共用
pkg/clientip/            按可信代理解析客户端 IP
pkg/telemetry/           OpenTelemetry 初始化（Prometheus 指标导出、OTLP Trace 导出）、Kratos 指标与链路中间件、带 trace_id 的日志 Handler；
                         telemetrytest 供单元测试读取指标
tools/jwtkey/            生成开发用 JWT 密钥与 media 下载地址签名密钥（make keys）
scripts/                 运维脚本（make grant-admin）
deploy/                  依赖组件的初始化脚本与配置：MySQL 初始化、APISIX、Prometheus 与告警规则、Alertmanager、Grafana、Jaeger
test/e2e/                依赖 compose 环境的端到端测试（构建标签 e2e）
test/cluster/            容器模式的集群测试与故障实验（构建标签 cluster）
test/testkit/            e2e 与集群测试共用的调用、注册、WebSocket 与 Redis 读取函数
test/load/k6/            k6 压测脚本（make load-*）
web/                     前端（React + Vite，pnpm，ADR-0008）；src/gen/ 为 buf 生成的接口 TS 代码，目录约定见前端设计 3.2 节
data/media/              media 本地存储的文件（运行时创建，被 Git 忽略）
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

以 account 为例（[main.go](../../app/account/cmd/account/main.go)），social 相同：

1. 读取 `-conf` 指定的配置目录（默认 `app/account/configs`），解析到 `conf.Bootstrap` 并按 protovalidate 规则校验，不合法时启动失败。配置对象保持打开，用于热更新。
2. 按 `log` 配置创建 `slog` 日志器，设为 Kratos 默认日志器；再按 `telemetry` 段初始化 OpenTelemetry（见 5.11），停机时最后关闭，导出剩余的 Span。
3. 用 `policy` 段创建 `biz.PolicyHolder`，并监听配置文件的 `policy` 段（见 5.1）。
4. `wireApp` 创建：Consul 注册中心、Redis 客户端（ping 一次）、MySQL 连接池（`gorm.Open` 立即 ping）、snowflake 生成器（抢占节点号租约）、各 repo 与用例、gRPC 与 HTTP 服务器。令牌密钥不合法、公钥与私钥不匹配、`revocation_ttl` 小于访问令牌有效期、固定验证码配在非 `log` 发送方上时都会启动失败。任一依赖不可用时退出，退出码为 1。布隆过滤器（account 的用户 ID、social 的群 ID）未就绪时在后台重建，不阻塞启动。social 的 account 客户端是惰性连接，account 不可用不影响 social 启动。
5. `app.Run()` 启动服务器并注册到 Consul；收到 SIGINT/SIGTERM 后先从 Consul 注销，再停止服务器，最后按创建的逆序释放节点号租约、关闭 MySQL、Redis。

其他服务的差异：

- account、social、message 的 Outbox 发布器和 Kafka 消费者（`pkg/outbox.Publisher`、`pkg/kafka.Consumer`）也实现为 Kratos 的 `transport.Server`，随 `app.Run()` 启停。Kratos 传给 `Start` 的 context 在停机时不会取消，所以它们各自持有 context，`Stop` 时取消。Kafka 不可用不影响启动：事件留在 Outbox 表中，消费者在后台重连。
- message 额外创建 social、account 两个惰性 gRPC 客户端，以及消费 social 群事件的消费者。
- access 不连 MySQL。连接管理器 `biz.Hub` 同样是 `transport.Server`。通话信令由 `biz.Calls` 处理，Hub 持有它：Calls 要把帧直接放进本实例连接的发送队列，两者互相引用，wire 不允许构造时成环，所以由 `NewHub` 设置 Calls 的反向引用。access 额外创建 message、social 两个惰性 gRPC 客户端，以及转发通话信令的 access 直连客户端。WebSocket 处理函数注册在 Kratos HTTP 服务器上，但连接的生命周期不使用请求 context，因为 Kratos 给它加了服务端超时（有测试覆盖）。停机顺序：
  1. Kratos 从 Consul 注销，网关约 1 秒内不再把新连接分到这个实例；
  2. Kratos 并发停止各服务器：HTTP 服务器关闭监听（`http.Server.Shutdown` 不管已劫持的 WebSocket 连接）；Hub 拒绝新连接，把现有连接以 1001 分批关闭，分布在 `websocket.drain_window`（默认 5 秒）内，连接关闭时删除路由；gRPC 服务器由 `server.DrainingGRPCServer` 包装，等 Hub 排空后才停止，排空期间发往剩余连接的推送仍然有效；
  3. 每个服务器的停机超时为 15 秒（`kratos.StopTimeout`），超时后强制断开剩余连接。Compose 中 access 的 `stop_grace_period` 为 20 秒。
  4. 连接关闭时结束它参与的通话；Hub 停止前取消本实例的振铃定时器，此时仍在振铃的通话按 Redis TTL 过期。
- delivery 有 HTTP 服务器（探针与 `/metrics`，步骤 8 新增）、gRPC 服务器（健康检查）、消费者，以及统计 Kafka 积压的 `kafka.LagMonitor`（见 5.11）；`policy` 段（推送冷却期）热更新。
- media 不连 Kafka、没有 Outbox。启动时按 `storage.provider` 创建文件存储：`local` 创建目录并试写一个临时文件，目录不可写或签名密钥少于 32 个字符时启动失败；`oss` 只检查配置是否完整，不访问网络。HTTP 服务器超时为 300 秒（Kratos 对所有路由使用同一个超时，要覆盖 100 MB 上传）。

## 5. 横切约定

### 5.1 配置

- 配置结构用 protobuf 定义：共用片段在 [pkg/conf/conf.proto](../../pkg/conf/conf.proto)（`Server`、`Registry`、`MySQL`、`Redis`、`Log`），每个服务的 `internal/conf/conf.proto` 用它们组合出自己的 `Bootstrap`。新增服务专属配置时在服务自己的 proto 中定义。
- 配置文件中的 `${XXX}` 引用环境变量 `MARSCHAT_XXX`，`${XXX:默认值}` 在变量缺失时取默认值。默认值对应本地开发环境，密码只能来自环境变量。引用的变量缺失且没有默认值时会替换成空字符串，不会报错，密码缺失会在连接依赖时失败。
- 时长字段按 protobuf `Duration` 的 JSON 格式书写，单位只能是秒（`0.5s`、`3600s`），写成 `500ms`、`1h` 会导致启动失败。每个服务都有解析真实配置文件的单元测试。
- 配置结构用 protovalidate 规则约束（例如限额必须大于 0），加载时校验。
- **热更新**：`bootstrap.WatchConfig` 监听某一段配置，文件变化后重新解析、校验，通过才原子替换，不通过时保留旧值并记录错误日志。目前热更新的有 account 的 `policy` 段（邮箱验证码与登录防护的限额）、social 的 `policy` 段（申请、建群频率与群成员上限）、message 的 `policy` 段（发消息频率、补拉上限与 `TOO_LONG` 阈值）、access 的 `policy` 段（每连接上行帧频率）和 media 的 `policy` 段（各用途的大小上限、图片像素上限、缩略图尺寸、下载地址有效期、上传频率），读取方每次使用时取一次快照；其余配置修改后需要重启。

### 5.2 日志

- 统一用标准库 `log/slog`，由 `bootstrap.NewLogger` 创建，输出到 stdout，格式 `text` 或 `json`，每条日志带 `service.name`、`service.version`。
- GORM 日志接入同一个日志器（`component=gorm`），只记录慢查询（超过 `mysql.slow_threshold`）和错误，查不到记录不算错误；SQL 只打印占位符，不打印参数。
- 访问日志由 `pkg/middleware/accesslog` 输出（`msg="server request"`），只记操作名、状态码、原因和耗时，**不记录请求体**：登录、注册请求里有密码、验证码和刷新令牌。503（依赖故障，例如节点号租约失效、下游服务不可用、熔断打开）记 WARN：它可预期、可恢复，根因由依赖侧的日志记录（拨号保护、`idgen` 续期失败等）；其他 5xx 记 ERROR，其余记 INFO。不使用 Kratos 自带的 `logging` 和 `recovery` 中间件，它们会打印请求体，见 [lessons/kratos-v3-server-defaults.md](../lessons/kratos-v3-server-defaults.md)。
- 日志 Handler 外面包了一层 `telemetry.LogHandler`：context 中有有效的 Span 时追加 `trace_id`、`span_id`，可以从日志找到 Jaeger 中对应的 Trace。记日志时要用 `InfoContext`、`WarnContext`、`ErrorContext` 并传入请求的 context，否则拿不到它们。
- 日志中的邮箱用 `biz.MaskEmail` 脱敏为 `u***@example.com`（保留首字符和域名）。
- go-redis 的内部日志（默认用标准库 `log` 写纯文本到 stderr）由 `bootstrap.NewRedis` 通过 `redis.SetLogger` 转到 `slog`（`msg="go-redis internal log"`，原文在 `detail`）。go-redis 不给级别，按内容分级：连接池拨号失败记 DEBUG（拨号保护已在故障开始和恢复时各记一条），内部恢复的 panic 记 ERROR，其他记 WARN。

### 5.3 服务注册与发现

- 注册中心是 Consul，服务名为 `marschat-<服务>`。实例 ID 为 `<服务名>-<主机名>-<进程号>`：Consul 要求同一 agent 上的实例 ID 唯一，本地多个服务共用一台主机，只用主机名会互相覆盖。
- 实例注册时登记 HTTP 和 gRPC 两个端点，带 TTL 心跳检查（宿主机运行时每 10 秒上报，20 秒未上报变为 critical，critical 持续 10 分钟后注销）。发现方只拿到检查通过的实例。
- `kratos.Server` 中 HTTP 服务器排在第一个（包括步骤 8 起有 HTTP 服务器的 delivery）：Kratos 把第一个端点登记为 Consul 服务的 `Address:Port`，网关的 Consul 发现按它转发 HTTP；各端点同时按协议写进 `TaggedAddresses`，Kratos 的 gRPC 客户端从中取 `grpc` 端点，不受顺序影响。
- `registry.consul.tcp_check_interval` 控制 Consul 主动 TCP 探测服务端口的间隔，同时是 TTL 心跳间隔。宿主机运行时为 `0s`（不探测）：服务监听 `127.0.0.1`，容器里的 Consul 探测不到。容器模式为 3 秒，实例被杀后几秒内从发现结果中消失。原来的布尔字段 `health_check` 无法用 `${XXX:默认值}` 占位符覆盖（替换结果是字符串，protojson 不接受字符串形式的布尔值），所以改成时长字段。
- 服务间调用用 Kratos gRPC 客户端，地址写成 `discovery:///marschat-<服务>`，负载均衡为加权轮询（wrr）。目前的调用：

  | 调用方 → 被调用方 | 接口 | 超时 |
  |---|---|---|
  | social → account | `BatchGetUsers`（好友、成员列表补全资料） | 1 秒 |
  | message → social | `CheckSendPermission`、`BatchGetGroups` | 1 秒 |
  | message → account | `BatchGetUsers`（会话列表补全对方资料） | 1 秒 |
  | message → media | `GetFiles`（发送图片、文件消息时核实文件，文本消息不调用） | 1 秒 |
  | access → message | `SendMessage` | 3 秒 |
  | access → social | `CheckSendPermission`（发起呼叫前） | 1 秒 |
  | delivery → social | `ListAllGroupMembers` | 1 秒 |
  | delivery → access | `Push`、`Kick`，**按在线路由中的地址直连**，不经 Consul，每个地址缓存一条连接 | 2 秒 |
  | access → access | `Push`（转发通话信令），同样按地址直连；目标是本实例时直接放进发送队列 | 2 秒（`call.forward_timeout`） |

  共同点：
  - 超时同时是服务发现首次解析的等待上限，并覆盖包括重试在内的整次调用。
  - 经 Consul 的调用都挂客户端中间件，从外到内为链路追踪、指标（见 5.11）、重试、熔断：
    - 重试（`pkg/middleware/retry`）：只对客户端列出的幂等方法（上表全部经 Consul 的接口；`SendMessage` 按 `client_msg_id` 幂等），只在 503 且不带错误原因（gRPC `Unavailable`，即连接级失败）时重试一次，间隔 20～50 毫秒，剩余截止时间不足 150 毫秒时不重试。业务错误、超时、下游返回的 `DEPENDENCY_UNAVAILABLE`、`CIRCUITBREAKER`、`no_available_node` 都不重试。Kratos 的 gRPC 负载均衡只在 READY 的连接中选择，实例退出后重试通常落到其他实例。
    - 熔断（Kratos `middleware/circuitbreaker`）：Google SRE 自适应限流，按接口统计，把 500、503、504 计为失败；打开时直接返回 `503 CIRCUITBREAKER`，请求不发出。
  - 直连 access 的调用（`pkg/accessclient`）只挂链路追踪和指标，不重试、不熔断：熔断按接口统计，一个 access 实例宕机会让发往所有实例的推送一起被熔断。调用返回 `Unavailable` 或 `DeadlineExceeded` 时，关闭并移出该地址的连接，地址进入冷却期（delivery 为 `policy.push_cooldown`，access 为 `call.forward_cooldown`，默认都是 5 秒），冷却期内发往它的调用直接跳过（记 DEBUG），冷却结束后重新拨号。不监听 Consul 中 access 的变化，因为正在排空的实例已经注销，但还在给剩余连接推送。
  - 连接惰性建立（`grpc.NewClient`），被调用方暂时不可用不影响调用方启动；就绪探针也不检查它，被调用方故障时调用方降级运行，而不是被摘掉流量。
  - 失败时 data 层统一包装成 `biz.ErrUnavailable`，由 biz 决定是返回 `503 DEPENDENCY_UNAVAILABLE`（写操作需要的校验）还是降级（列表资料补全留空并记 WARN）。
- Kratos v3.0.0 的 gRPC 负载均衡器在包初始化时就读取了全局选择器，而默认选择器设置得更晚，经服务发现调用会空指针 panic。`pkg/balancer` 在更早的初始化阶段设置选择器，`pkg/bootstrap` 匿名导入了它；不使用 bootstrap 的程序（如 e2e 测试）要自己匿名导入 `pkg/balancer`。原因与验证见 [lessons/kratos-grpc-selector-nil.md](../lessons/kratos-grpc-selector-nil.md)。

### 5.4 中间件与鉴权

HTTP 与 gRPC 共用同一条中间件链，由 [bootstrap.ServerMiddlewares](../../pkg/bootstrap/middleware.go) 组装，各服务只提供“哪些操作需要登录”的判断函数（account 用公开接口白名单，social 的对外接口全部需要登录）。从外到内：

1. 链路追踪（Kratos contrib/otel 的 `tracing.Server`）：从请求头恢复上游的 Trace 上下文并开启服务端 Span。放在最外层，后面的中间件和访问日志都能拿到 trace_id。
2. 指标（`metrics.Server`）：按操作统计请求数（带状态码与错误原因）和耗时。
3. `accesslog`：访问日志、panic 恢复；非 Kratos 错误（数据库、Redis 等）记日志后换成 `500 INTERNAL`，不把内部信息返回给客户端。
4. 令牌校验 `pkg/auth.Server`，用 Kratos `selector` 只作用于需要登录的接口。公开接口用白名单列出，新增的接口默认需要登录。
5. 参数校验：Kratos validate contrib，按 proto 中的 protovalidate 规则校验，失败返回 `400 VALIDATOR`。先鉴权后校验，未登录的请求拿不到参数规则信息。

访问令牌：

- JWT，Ed25519（EdDSA）签名，有效期 15 分钟。account 持有私钥签发，其他服务只配置公钥（`auth` 配置段，各服务相同）。
- 校验：解析阶段只接受 EdDSA（防算法混淆），检查签发方、受众、过期时间和签发时间；再用一次 `MGET` 查 Redis 吊销名单（会话级、用户级），最后把 `auth.Principal`（用户 ID、会话 ID、角色、签发时间）放进 context，业务代码用 `auth.FromContext` 取。
- Redis 故障时吊销检查放行并记 WARN（开发者确认的取舍）。
- 刷新令牌、吊销名单的细节见 [account 设计 3.2](../design/account-service.md#32-令牌)，键见 [data-model.md](data-model.md#3-redis-键)。
- 内部接口（`AccountInternalService`、`SocialInternalService`、`MessageInternalService`、`AccessInternalService`、`MediaInternalService`）只注册在 gRPC 上，不经过令牌校验；服务网络不对外暴露，服务间认证见 TD-26。
- WebSocket 握手时浏览器不能带 `Authorization` 头，令牌放在第一帧，由 `pkg/auth.Verifier` 校验。它与中间件共用解析选项和吊销检查，单测让每个令牌用例同时经过两条路径并比对结论。连接建立后每次心跳用 `Verifier.IsRevoked` 复查吊销名单，见 [data-flow.md](data-flow.md#32-心跳与失效)。

自定义 HTTP 处理函数：media 的上传（请求体是文件原始字节）和本地存储的下载内容不适合 protobuf JSON，不在 proto 中声明，用 `srv.Route("/").POST/GET` 注册。处理函数与生成代码的写法相同：先 `khttp.SetOperation` 设置操作名（`/media.v1.MediaService/UploadFile`、`/media.v1.MediaService/DownloadContent`），再把查询参数绑定到 proto 消息，经 `ctx.Middleware` 走同一条中间件链，访问日志、鉴权、参数校验和错误格式都与其他接口一致。下载内容由地址签名授权（浏览器的 `<img>` 带不了 `Authorization` 头），在 media 的鉴权白名单中。

客户端 IP：`pkg/clientip` 只在请求来自 `security.trusted_proxies` 中的地址时才读取 `X-Forwarded-For`，并取最右边一个不可信的地址；默认不信任任何代理，步骤 5 填入网关地址。

HTTP 编码：请求和响应按 protobuf JSON 规则（字段名 lowerCamelCase，枚举为名字字符串，int64 为字符串，Timestamp 为 RFC 3339，零值字段也输出）。Kratos v3 的 `json` codec 默认不这样做，由 `pkg/jsoncodec` 覆盖，`pkg/bootstrap` 已匿名导入，见 [lessons/kratos-v3-server-defaults.md](../lessons/kratos-v3-server-defaults.md)。客户端必须带 `Content-Type: application/json`，否则 Kratos 按表单解析。

### 5.5 健康检查

| 探针 | 位置 | 行为 |
|---|---|---|
| 存活 | HTTP `GET /healthz` | 进程能处理请求就返回 200，不检查依赖，依赖故障时重启进程没有意义 |
| 就绪 | HTTP `GET /readyz` | 检查本服务的存储（account、social、message、media 为 MySQL 与 Redis，access 只有 Redis；media 不检查文件存储，OSS 暂时不可达时下载地址照常签发），全部可用返回 200，否则返回 503 和失败的依赖名；错误详情只写日志。delivery 检查 Redis（在线路由）。被调用的其他服务和 Kafka 不在其中：它们故障时服务降级运行，不应被摘掉流量 |
| gRPC | `grpc.health.v1.Health` | Kratos 默认注册，服务启动后为 SERVING，停止时为 NOT_SERVING |

### 5.6 代码生成

| 内容 | 命令 | 说明 |
|---|---|---|
| 配置 proto | `make conf` | `buf generate --template buf.gen.conf.yaml`，插件 `protoc-gen-go` 用 `go run` 固定版本 |
| 接口 proto | `make api` | `buf generate --template buf.gen.yaml`：Go 消息、gRPC、Kratos HTTP 路由，以及合并的 `api/openapi.yaml`；插件同样用 `go run` 固定版本 |
| 依赖注入 | `make wire` | `wire` v0.7.0 |
| 前端 TS | `make web-gen` | `buf generate --template buf.gen.web.yaml`：先删除 `web/src/gen/` 再生成；插件 `protoc-gen-es` 来自 `web/node_modules`，版本由 `web/package.json` 锁定，用 `node` 直接运行插件入口（Windows 上 buf 不能执行 `node_modules/.bin` 下的 sh 脚本）；`include_imports` 一并生成 `buf/validate`、`google/api` |
| 全部 | `make gen` | 以上全部；需要先 `make web-install`。CI 会重新生成并检查与提交内容一致 |

`buf.yaml` 有三个模块，lint 都使用 STANDARD 规则：

- `api`：接口契约。`error_reason.proto` 例外，不要求枚举值带类型前缀，让错误原因保持 `USER_NOT_FOUND` 这样的短名字。
- `pkg`、`app`：配置 proto，例外是不要求版本后缀和“包名与目录一致”（配置不是对外契约，包名统一加 `marschat.` 前缀以免与依赖冲突）。
- 依赖 `buf.build/googleapis/googleapis`（HTTP 注解）和 `buf.build/bufbuild/protovalidate`（校验规则），版本锁定在 `buf.lock`。
- 兼容性检查规则为 `FILE`：CI 中与基准分支（PR）或上一次推送比较，删除字段、修改字段号等破坏已生成代码的改动会失败。

### 5.7 缓存与布隆过滤器

- Redis 客户端由 `pkg/bootstrap.NewRedis` 创建，带拨号保护：拨号失败后把 Redis 标记为不可达，期间的拨号直接返回上次的错误，后台每秒探测一次，探测成功后恢复；单次拨号不重试（`DialerRetries: 1`）。下面各处“Redis 故障时放行、回源、只记 WARN”的降级，都依赖命令在调用方的截止时间内失败。没有这层保护时，容器停止后解析服务名约 1 秒才失败，加上 go-redis 默认的拨号重试与命令重试，一条命令要耗几秒，见 [lessons/redis-dial-stall-docker-dns.md](../lessons/redis-dial-stall-docker-dns.md)。
- 读缓存统一用 `pkg/cache`（Cache-Aside）：空值标记防穿透、singleflight 合并本实例的并发回源、TTL ±10% 抖动、Redis 故障时直接回源。写操作先提交数据库，再删除缓存；删除失败记 WARN。social 的群资料与成员列表还会在消费自己的群事件时再删一次（消费组 `marschat-social-cache`，Redis 故障时重试到成功）；account 的资料缓存没有对应事件，靠 TTL 兜底（TD-25）。
- 热点键开启 `Options.RebuildLock`（目前只有群成员列表）：未命中时抢 `{key}:lock`（`SET NX PX 3000`），抢到的实例回源、回填后校验持有者再释放；没抢到的每 50 毫秒重读一次，最多 200 毫秒，仍未命中就直接回源。宁可多一次查询，也不让请求失败。
- media 的文件元数据写入后不再修改，缓存只回填、不删除；文件 ID 不对外列举，只用空值标记防穿透，不加布隆过滤器。
- 按 ID 查询、容易被随机 ID 穿透的数据（用户、群）在缓存未命中时先查 `pkg/bloom`。只有就绪标记和过滤器都在时才信任“不存在”；新 ID 在提交后加入，失败重试仍不成功则写脏标记并重建；多实例用 Redis 锁保证只有一个在重建。批量接口的 ID 来自内部数据，不查过滤器。
- `BF.EXISTS` 在 go-redis v9 默认的 RESP3 下返回布尔值，读取用 `Cmd.Bool()`，见 [lessons/redis-resp3-reply-types.md](../lessons/redis-resp3-reply-types.md)。

### 5.8 错误

- 业务错误用 Kratos errors，错误原因在各服务 proto 的 `ErrorReason` 枚举中定义（[account](../../api/account/v1/error_reason.proto)、[social](../../api/social/v1/error_reason.proto)、[message](../../api/message/v1/error_reason.proto)、[access](../../api/access/v1/error_reason.proto)、[media](../../api/media/v1/error_reason.proto)），在 biz 层的 `errors.go` 中手写构造函数，不使用 errors 代码生成器。HTTP 状态码随错误类型确定：参数与验证码错误 400、凭据无效 401、封禁与无权限 403、不存在 404、冲突 409、限流 429、上传缺少长度 411、文件过大 413、类型不支持 415、依赖故障 503。
- 发号（`pkg/idgen`）在节点号租约失效时返回 `idgen.ErrLeaseLost`（Redis 停机超过 30 秒），各服务 biz 层的 `nextID` 把它转成 `503 DEPENDENCY_UNAVAILABLE`，客户端稍后重试；其他发号错误（时钟大幅回拨）仍按内部错误处理。account 注册时先发号再校验验证码，租约失效不会白白消耗验证码。
- 错误体为 `{"code", "reason", "message", "metadata"}`。`message` 是给用户看的中文提示；客户端按 `reason` 分支处理，不解析 `message`。
- 限流类错误（429）在 `metadata.retry_after_seconds` 中给出需要等待的秒数。
- 框架产生的错误：令牌缺失、无效、过期或已吊销统一为 `401 UNAUTHORIZED`；参数不合法为 `400 VALIDATOR`；请求体无法解码为 `400 CODEC`；内部错误为 `500 INTERNAL`，不带细节。
- 调用其他服务时，被调用方返回的业务错误（带错误原因）原样透传给客户端，例如发消息时 social 的 `NOT_FRIENDS`；超时、连接失败等没有错误原因的错误由调用方包装成自己的 503（message 的 `DEPENDENCY_UNAVAILABLE`、access 的 `UPSTREAM_UNAVAILABLE`），原始错误只进日志。
- access 调 message 时，熔断打开的 `503 CIRCUITBREAKER` 带错误原因，同样原样透传：它表示消息确定没有发出，客户端可以稍后用同一个 `client_msg_id` 重发；`UPSTREAM_UNAVAILABLE` 表示结果未知，重发由 `client_msg_id` 去重。message 调 social、account 时熔断打开按依赖不可用处理（`DEPENDENCY_UNAVAILABLE` 或降级）。
- WebSocket 上的 `Error` 帧与 HTTP 错误体字段相同。

### 5.9 异步消息

- 跨服务的状态变化用事件通知：业务事务内写 `outbox_events`，`pkg/outbox.Publisher` 发布到 Kafka（topic `marschat.<服务>.events`，记录头带事件类型、事件 ID、来源），消费方用 `pkg/kafka.Consumer` 按事件类型注册处理器（`kafka.On[T]` 负责解码）。
- 事件类型是 proto 全名，定义在各服务的 `api/<服务>/v1/events.proto`；topic 由 `make up` 中的 `kafka-init` 一次性创建，broker 关闭了自动建 topic。
- 每个服务只有持有 Redis 租约（`outbox:lease:{source}`）的实例发布。事务提交后 `Notify` 立即唤醒发布器；本实例不持有租约时，再向 Redis 频道 `outbox:wake:{source}` 发一条消息，持有者订阅该频道，收到后立即发布。唤醒丢失只是退回按 `outbox.poll_interval`（默认配置 0.5 秒）轮询。
- 处理器要幂等：Outbox 至少发布一次，消费者重启、再均衡后也可能重复投递。需要按顺序生效的处理器（群成员投影）返回错误时原地无限重试；不要求顺序但要求最终成功的处理器（缓存删除）配置重试队列（`ConsumerConfig.RetryTopic`），原地尝试 3 次后转入重试 topic，10 秒后由重试消费者再处理；尽力而为的处理器（推送、通知）只记日志、从不返回错误。
- 处理不了的记录（`kafka.Permanent`、解码失败、panic）写入共用的死信队列 `marschat.dlq` 后跳过，写入失败时不提交位点。重试与死信的参数在代码中设置，不是限额，不做热更新。
- 流转、topic 与消费组一览见 [data-flow.md](data-flow.md#6-事件与-outbox)。

### 5.10 测试

- 单元测试不依赖外部服务，`go test ./...` 即可运行。biz 层用内存假 repo；Redis 相关逻辑（Lua 脚本、限流、缓存与重建锁、ID 租约、吊销名单、Outbox 租约、在线路由的字段级 TTL）用 miniredis。access 的连接管理用 `httptest` 加真实的 gorilla/websocket 客户端测试；media 的自定义处理函数用真实的 Kratos HTTP 服务器、中间件链和本地存储测试（`app/media/internal/server/http_test.go`），OSS 实现用 `httptest` 假服务器验证请求、离线验证预签名地址；Kafka 消费者和 Outbox 发布器用假的拉取与发送接口测试。
- miniredis 不支持 RedisBloom，`pkg/bloom` 的测试在 `test/e2e/bloom_test.go`，连真实 Redis，用“把过滤器的键改成字符串”注入 `BF.INSERT` 失败。
- 依赖 compose 环境和已启动服务的测试放在 `test/e2e`，带 `e2e` 构建标签，用 `make e2e` 运行；它会加载 `.env`，测试用其中的密码直接连 Redis、MySQL 做准备工作（清理本机 IP 的限流计数、设置管理员）。
- 容器模式的集群测试与故障实验放在 `test/cluster`，带 `cluster` 构建标签，用 `make cluster-test` 运行，客户端只经网关访问。两组测试共用 `test/testkit` 中的 HTTP 调用、注册、WebSocket 收发和 Redis 读取函数，区别只在地址。
- 故障实验（`test/cluster/fault_test.go`）在持续收发时用 `docker kill`、`restart`、`pause`、`stop` 注入故障，只操作 `marschat-` 前缀的容器，结束时（包括失败）恢复容器并等待其在 Consul 中恢复 passing 或健康检查通过。测试客户端（`session_test.go`）按客户端约定处理断线：随机等待 0～2 秒后经网关重连，从本地连续的最大 seq 补拉，按 seq 去重。每个实验最后检查历史消息恰好是 1..N、`client_msg_id` 不重复，接收方经推送和补拉拿到的 seq 连续。实验结果记在[重构计划](../plans/completed/backend-refactor.md#故障实验结果)；`go test -short` 跳过这些实验。
- 指标与 Trace 的单元测试：`pkg/telemetry/telemetrytest` 把全局 MeterProvider 换成 ManualReader（每个测试二进制只设置一次，用例按增量比较），Span 用 SDK 的内存导出器读取。
- 压测脚本在 `test/load/k6`（k6，经网关），用 `make load-*` 运行，做法见 [deployment.md](../deployment.md#压测)，结果记在重构计划。

### 5.11 可观测性

设计、指标全表与告警规则见 [observability.md](../design/observability.md)。

- **初始化**：`telemetry.Setup` 按 `telemetry` 配置段创建 MeterProvider 和 TracerProvider，资源属性为 `service.name`、`service.version`、`service.instance.id`（与 Consul 中的实例 ID 相同）。传播格式固定为 W3C Trace Context。导出失败（例如 Jaeger 没有启动）不影响请求，SDK 的错误每分钟最多记一条 WARN（`msg="telemetry error"`，带被抑制的次数）。
- **指标**：OpenTelemetry 指标经 Prometheus 导出器注册到进程内的默认注册表，与 Go 运行时、进程指标一起由各服务 HTTP 端口的 `/metrics` 暴露，不经网关。名称按 Prometheus 规范转换（加单位和 `_total` 后缀）。
  - 接口：Kratos 指标中间件的 `server_requests_code_total`、`server_requests_seconds`（服务端）和 `client_requests_*`（gRPC 客户端），直方图桶为 `telemetry.LatencyBuckets`（1 毫秒～10 秒）。
  - 业务：`marschat_` 前缀，包括 WebSocket 连接数、关闭原因与上行帧（access）、通话事件（access）、推送结果与消息投递延迟（delivery）、Kafka 消费结果与处理耗时（`pkg/kafka`）、Outbox 发布结果与延迟（`pkg/outbox`）、缓存命中与回源（`pkg/cache`，按 `Options.Name` 区分）、布隆过滤器判定（`pkg/bloom`）、限流结果（`pkg/ratelimit`）。
  - Kafka 积压：delivery 的 `kafka.LagMonitor` 用 kadm 每 15 秒查询一次全部 `marschat-` 消费组，`marschat_kafka_consumer_lag` 读取缓存的结果。查询失败时清空缓存（指标消失，而不是报告旧值），只在状态变化时记日志。两个 delivery 实例报告相同的值，面板和告警按 `max` 聚合；delivery 全部停止时指标随之消失（TD-42）。
- **链路追踪**：
  - 只在配置了 `telemetry.otlp_endpoint` 时导出（OTLP gRPC，批量发送），容器模式为 `jaeger:4317`。采样为 `ParentBased(TraceIDRatioBased(sample_ratio))`，默认 1，压测时调低。
  - HTTP、gRPC 的服务端与客户端由 Kratos tracing 中间件开启 Span。access 为每个 `SendMessage`、`Call` 上行帧开启服务端 Span（`ws.SendMessage`、`ws.Call`），带用户、连接与请求 ID。
  - 跨 Kafka：`outbox.Add` 把 context 中的 Trace 上下文写入 `outbox_events.trace_parent`；发布器为每条事件开启 `publish <topic>` 生产者 Span（起点为事件写入时刻，覆盖在 Outbox 中等待的时间）并注入 Kafka 记录头 `traceparent`；消费者从记录头恢复，开启覆盖全部原地重试的 `consume <topic>` 消费者 Span，转入重试队列和死信队列的记录保留这个头。流转见 [data-flow.md](data-flow.md#10-链路追踪)。
  - 消息相关的 Span 带 `marschat.conversation_id`、`marschat.seq`（二者唯一标识一条消息），发送路径还带 `marschat.client_msg_id`，可以在 Jaeger 中按它们检索。
- **告警**：规则在 `deploy/prometheus/rules.yml`（实例不可达、5xx 比例、P99、Kafka 积压、死信、推送失败率、Outbox 发布延迟、消息投递延迟），单元测试在 `rules_test.yml`（`make rules-test`）。告警只在 Alertmanager 和 Grafana 中查看，不发外部通知。
- **面板**：Grafana 预置“MarsChat 总览”（`deploy/grafana/dashboards/`）。Trace 在 Jaeger UI 中查看：Grafana 13 的 Jaeger 数据源与 Jaeger 2.23 的查询接口不兼容，没有配置。

## 6. 关键技术组件

| 组件 | 版本 | 用途 |
|---|---|---|
| Go | 1.27.3 | 后端语言 |
| Kratos | v3.0.0；contrib（registry/consul、middleware/jwt、middleware/validate、otel）v3 伪版本 `v3.0.0-20261026152050-dd0fb7df1e32` | 微服务框架、Consul 注册发现、JWT 与参数校验中间件、指标与链路追踪中间件 |
| OpenTelemetry Go | v1.47.0（API、SDK、OTLP gRPC Trace 导出器）；Prometheus 导出器 v0.69.0 | 指标与链路追踪 |
| prometheus/client_golang | v1.25.0 | `/metrics` 处理函数与默认注册表 |
| golang-jwt | v5.3.1 | JWT 解析与签名 |
| protovalidate | v1.2.0 | 接口参数与配置校验 |
| franz-go | v1.22.3；kadm v1.20.0 | Kafka 客户端（幂等生产者、消费组手动提交）；kadm 查询消费组积压 |
| gorilla/websocket | v1.5.3 | WebSocket（access） |
| golang.org/x/crypto | v0.59.0 | bcrypt |
| golang.org/x/sync | v0.25.0 | singleflight |
| golang.org/x/image | v0.48.0 | 缩略图缩放（CatmullRom）与 WebP 解码（media） |
| alibabacloud-oss-go-sdk-v2 | v1.6.1 | 阿里云 OSS 官方 SDK（media 的 oss 存储） |
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
