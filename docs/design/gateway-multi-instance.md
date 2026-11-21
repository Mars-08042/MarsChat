# 设计：网关与多实例（重构步骤 5）

- 状态：已采纳（2026-11-19 开发者确认，第 8 节的 5 个问题均按推荐方案） | 日期：2026-11-19 | 关联：[重构计划步骤 5](../plans/active/backend-refactor.md#5-网关与多实例)、[ADR-0005](../architecture/adr/0005-distributed-architecture.md)、[ADR-0006](../architecture/adr/0006-message-sync-model.md)、[消息链路设计](message-pipeline.md)、[技术债](../plans/tech-debt.md)

## 1. 背景与目标

步骤 4 完成了消息主链路。它在设计上支持多实例，但每个服务只跑过一个实例，下面这些都没有验证过：

- 跨 access 实例推送；
- Outbox 租约在实例之间接管；
- 实例故障与滚动发布时客户端能否补齐消息。

服务目前运行在宿主机，客户端直接访问各服务的端口，没有统一入口、TLS 和入口限流。服务之间的调用只有超时，没有重试和熔断。另外还有几项遗留问题要在本步骤处理：

- Kafka 消费失败只能原地重试（TD-30）；
- delivery 缓存的 access 连接不会摘除（TD-31）；
- Outbox 租约可能短暂出现两个发布者（TD-32）。

目标：

- **服务容器化与多实例**：
  - 每个服务一个镜像，Compose 中 access、message、delivery 各起 2 个实例，account、social 各 1 个；
  - 宿主机运行模式保留，用于日常开发和 `make e2e`。
- **APISIX 网关**：
  - Standalone 模式，用 Consul 服务发现；
  - HTTP 按路径转发到各服务，WebSocket 转发到 access；
  - TLS（自签证书）和入口限流，限流配置修改后热生效；
  - 服务能拿到真实的客户端 IP。
- **调用韧性**：
  - 服务间调用保留超时；
  - 只对幂等调用重试一次；
  - 按接口熔断。
- **消费韧性**：
  - 加入死信队列，无序消费者加入重试队列（TD-30）；
  - delivery 推送失败时摘除连接并冷却（TD-31）；
  - Outbox 发布前检查剩余租期（TD-32）；
  - 节点号租约丢失后自动重新获取（新发现，见 2.3 节）。
- **access 优雅停机**：先从 Consul 摘除，再分批通知客户端重连。停机期间推送仍然可用。
- **social 实时通知**：好友申请、入群申请、成员变化等事件经 delivery 推送给在线用户。
- **故障实验**：
  - 实验项目：跨节点收发，杀掉 access、delivery 或持有 Outbox 租约的 message，滚动重启 access，暂停 Kafka，停掉 Redis；
  - 验证消息不丢、不重，重连后能补齐；
  - 结果记录在重构计划中。

不在本步骤：

- 服务间认证（TD-26），见第 8 节问题 4；
- 监控告警，在步骤 8 实现。本步骤只留日志，死信积压告警和消费积压告警留到步骤 8；
- 压测，在步骤 8 实现；
- media（步骤 6）；
- 通话信令（步骤 7）；
- 网关层的令牌校验。各服务已经自己校验令牌，网关只做转发、TLS 和限流。

## 2. 需求与约束

### 2.1 资源

Docker Desktop 虚拟机共 3.8 GiB 内存（2026-11-19 用 `docker info`、`docker stats` 实测）：

| 内容 | 内存 |
|---|---|
| MarsChat 依赖组件 | 约 1.64 GiB（Kafka 1.06 GiB、MySQL 485 MiB、Consul 74 MiB、Redis 20 MiB） |
| 本机其他项目的容器 | 约 0.43 GiB |

由此得出：

- 在虚拟机里执行 `go build` 编译整个 module 可能把内存用尽，所以镜像改为在宿主机交叉编译后打包（见 3.2 节）。
- Kafka 使用镜像默认的 1 GiB 堆，本地单 broker、四五个 topic 用不了这么多，建议限制为 512 MiB（第 8 节问题 2）。
- 估算新增内存约 0.45 GiB：8 个 Go 服务容器约 0.3 GiB，APISIX 约 0.15 GiB。限制 Kafka 堆后，总占用约 2.0 GiB。第一阶段实测并记录。

### 2.2 两种运行模式

| 模式 | 用途 | 入口 | 测试 |
|---|---|---|---|
| 宿主机（现有） | 日常开发、调试 | 各服务端口 `18xx0` | `make e2e` |
| 容器（新增，profile `apps`） | 多实例、网关、故障实验 | APISIX `127.0.0.1:19080`（HTTP）、`19443`（HTTPS） | `make cluster-test` |

两种模式共用同一个 Consul，服务名相同，**不能同时运行**：

- 宿主机实例注册的是 `127.0.0.1`，容器访问不到；
- 容器实例注册的是容器 IP，宿主机访问不到。

`make cluster-test` 开始前检查 Consul 中是否有地址为 `127.0.0.1` 的实例，有就报错退出。

应用容器不发布宿主机端口，只经网关访问。依赖组件的宿主机端口保留，测试仍可直接读 Redis 和 Kafka 来核对状态。

### 2.3 现状核对中发现的问题

| 问题 | 影响 | 处理 |
|---|---|---|
| Kratos 的 Consul 注册把第一个端点作为服务的 `Address:Port`，其他端点按协议写进 `TaggedAddresses`。各服务目前的顺序是 `kratos.Server(gs, hs, ...)`，所以 `Address:Port` 是 gRPC 端口 | APISIX 的 Consul 发现按 `Service.Address:Service.Port` 生成上游节点，会把 HTTP 请求转发到 gRPC 端口 | account、social、message、access 改为 HTTP 服务器在前。Kratos 的 gRPC 客户端按协议从 `TaggedAddresses` 取端点，不受顺序影响，现有的 `make e2e` 会经服务发现调用 gRPC，可作为回归验证 |
| Kratos 停机顺序：`BeforeStop` 钩子 → 从注册中心注销 → 所有服务器**并发**停止 | access 的 Hub 关闭连接时，gRPC 服务器已经在停止，正在关闭的连接收不到推送 | 用包装过的 gRPC 服务器等 Hub 排空后再停止，见 3.8 节 |
| `pkg/idgen` 的节点号租约为 30 秒。Redis 不可用超过 30 秒后发号返回 `ErrLeaseLost`；Redis 恢复后租约键已过期，续期脚本返回 0，代码按“被其他实例占用”处理，不再重新获取 | 停掉 Redis 超过 30 秒，account、social、message 在重启之前都无法生成 ID，写操作全部失败 | 新增技术债 TD-34，本步骤修复，见 3.6 节 |
| `access` 的 `advertise_addr` 默认是 `127.0.0.1:19100` | 多实例时无法给每个副本单独配置地址 | 默认改为留空，从 Kratos 已注册的 gRPC 端点自动获取，见 3.4 节 |

## 3. 方案

### 3.1 总体拓扑

```
浏览器 / 测试客户端
   │  HTTP 19080、HTTPS 19443（宿主机只绑定 127.0.0.1）
   ▼
APISIX（固定 IP 172.30.0.10，Standalone，Consul 发现）
   ├─ /v1/auth、/v1/users、/v1/sessions、/v1/admin ─→ account ×1（HTTP 18010）
   ├─ /v1/friend-requests、/v1/friends、/v1/blocks、/v1/groups、/v1/group-join-requests ─→ social ×1（HTTP 18020）
   ├─ /v1/conversations ─→ message ×2（HTTP 18030）
   └─ /ws（WebSocket）─→ access ×2（HTTP 18000）

服务之间：gRPC 经 Consul 发现（discovery:///marschat-xxx）；delivery 按在线路由直连 access 的 gRPC 19100
依赖：MySQL、Redis、Kafka（容器内用 kafka:29092）、Consul
```

Compose 网络改为固定子网 `172.30.0.0/24`（本机已有网络占用 172.17、18、21、22、24、25、26 网段），APISIX 用固定 IP `172.30.0.10`。account 的 `trusted_proxies` 只信任这一个地址，见 3.3 节。网络配置变化后，需要先 `docker compose down`（不加 `-v`，数据卷保留），再重建 MarsChat 的容器，见第 8 节问题 2。

### 3.2 服务镜像

**构建**：`make images` 在宿主机交叉编译，再打包镜像：

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o build/linux/<服务> ./app/<服务>/cmd/<服务>
docker build -f deploy/docker/Dockerfile --build-arg SERVICE=<服务> -t marschat/<服务>:dev build/linux
```

选择宿主机编译的原因：

- 虚拟机内存紧张（2.1 节）；
- 可以直接用宿主机的模块缓存和 Go 代理设置，镜像构建过程不需要联网下载依赖；
- 构建上下文只有二进制文件，几秒就能完成。

代价是镜像不能脱离宿主机独立构建。CI 需要构建镜像时，再补多阶段 Dockerfile。`build/` 目录加入 `.gitignore`。

**Dockerfile**：

- 运行镜像用 distroless static 的 nonroot 变体：只含 CA 证书、时区数据和非 root 用户，没有 shell。具体标签在实施时于本地拉取确认并锁定；第 8 节问题 1 给了 alpine 作为备选。
- 入口命令为 `/app/<服务> -conf /app/configs`。
- 配置目录以只读方式挂载宿主机的 `app/<服务>/configs`，不打进镜像。改配置不用重建镜像，限额策略可以热更新。Docker Desktop 的目录挂载能否把文件变化通知传进容器还未验证，见第 6 节。

**容器配置**：配置文件不变，用现有的 `MARSCHAT_` 环境变量覆盖地址：

| 变量 | 容器内的值 |
|---|---|
| `MARSCHAT_<服务>_HTTP_ADDR`、`MARSCHAT_<服务>_GRPC_ADDR` | `0.0.0.0:<端口>` |
| `MARSCHAT_MYSQL_ADDR` | `mysql:3306` |
| `MARSCHAT_REDIS_ADDR` | `redis:6379` |
| `MARSCHAT_KAFKA_BROKERS` | `kafka:29092` |
| `MARSCHAT_CONSUL_ADDR` | `consul:8500` |
| `MARSCHAT_CONSUL_HEALTH_CHECK` | `true` |
| `MARSCHAT_CONSUL_CHECK_INTERVAL` | `3s` |
| `MARSCHAT_LOG_FORMAT` | `json` |

- 每个服务只拿到自己需要的密钥，不用 `env_file` 把整个 `.env` 传进去。例如 JWT 私钥只给 account。
- 服务监听 `0.0.0.0` 时，Kratos 注册的端点取容器网卡的 IP。
- 应用容器设置 `mem_limit: 256m` 和 `stop_grace_period`；access 为 20 秒，其他服务为 10 秒。
- 多实例用 `deploy.replicas: 2`，容器名为 `marschat-access-1`、`marschat-access-2`。故障实验按容器名操作。

**Consul 检查**：

- 容器内开启 TCP 检查。本地开发时关闭它，是因为宿主机上的服务对 Consul 容器不可达，容器化后这个限制不存在。
- 新增配置 `registry.consul.check_interval`，默认 10 秒，即 Kratos 的默认值；容器内设为 3 秒。TTL 心跳为间隔的 2 倍。
- 实例被杀后约 3 到 6 秒内变为 critical，网关和各服务的发现结果随之去掉它。

### 3.3 APISIX 网关

**部署方式**：

- 镜像 `apache/apisix:3.20.0`，具体变体（如 `-debian`）在实施时于本地确认。使用镜像自带的入口命令；3.20 的发布说明提到，只以 `apisix start` 为前台命令的容器会在 OpenResty 转入后台后退出。
- `deploy/apisix/config.yaml`：
  - `deployment.role: data_plane`，`role_data_plane.config_provider: yaml`。不需要 etcd，也没有 Admin API；
  - `discovery.consul`：`servers: ["http://consul:8500"]`、`skip_services: ["consul"]`、长轮询 `timeout.wait`；
  - 监听 9080（HTTP）和 9443（HTTPS）。
- `deploy/apisix/apisix.yaml`：路由、上游、证书和全局规则，文件以 `#END` 结尾。APISIX 每秒检查一次这个文件，改动不用重启就会生效。

**Standalone 与 Consul 的组合**：

- 官方文档没有这种组合的示例，但从源码看，Consul 发现模块只依赖 Consul 的 HTTP 接口，与配置中心是 etcd 还是 YAML 文件无关。
- 第一阶段先做实验：两个 account 实例都能收到请求；杀掉一个后，它在几秒内被摘除。
- 实验不通过时，按 ADR-0005 的备选方案改用 Docker 内置 DNS：上游节点写 Compose 服务名，DNS 返回全部副本的 IP。这时摘除依赖 APISIX 的被动健康检查。

**路由**：

| 路径 | 上游（Consul 服务名） | 说明 |
|---|---|---|
| `/v1/auth/*`、`/v1/users/*`、`/v1/sessions`、`/v1/sessions/*`、`/v1/admin/*` | `marschat-account` | 管理接口由服务按令牌中的角色校验 |
| `/v1/friend-requests*`、`/v1/friends*`、`/v1/blocks*`、`/v1/groups*`、`/v1/group-join-requests/*` | `marschat-social` | |
| `/v1/conversations*` | `marschat-message` | |
| `/ws` | `marschat-access` | `enable_websocket: true` |

其他路径由 APISIX 返回 404。内部接口只注册在 gRPC 上，网关只转发到 HTTP 端口，所以从网关访问不到内部接口。

**上游参数**：

- HTTP 上游：`roundrobin`；超时为连接 2 秒、发送 5 秒、读取 5 秒；`retries: 1`。
  - APISIX 沿用 Nginx 的规则：连接失败或请求还没发到上游时才换节点重试；非幂等方法的请求一旦发出就不再重试，所以 POST 不会被重复执行。
  - 实例被杀、Consul 还没摘除的几秒内，请求靠这次重试转到另一个实例。
- WebSocket 上游：`least_conn`，让长连接在实例间均匀分布；读取超时 120 秒，大于客户端 30 秒的心跳间隔。
- 不用 3.19 新增的 `ws` 上游协议（逐帧处理）。access 的帧是二进制 protobuf，网关不需要解析，透明转发更简单。

**入口限流**：初始值如下，修改 `apisix.yaml` 即可热生效。超限返回 429。

| 范围 | 插件 | 初始值 | 目的 |
|---|---|---|---|
| 全部 HTTP 路由，按客户端 IP | `limit-req` | 50 次/秒，突发 100 | 挡住单个来源的洪泛 |
| `/v1/auth/*`，按客户端 IP | `limit-count` | 60 次/分钟 | 登录、验证码接口的第一道防线（服务内的验证码与登录限制保留） |
| `/ws`，按客户端 IP | `limit-conn` 与 `limit-req` | 并发 50；握手 10 次/秒 | 防止大量建连和重连风暴 |

- 计数用 `local` 策略，保存在 APISIX 进程内。只有一个网关实例，不需要 Redis 计数。
- 网关返回的 429 响应体格式是 APISIX 的 `{"error_msg": ...}`，与服务的 Kratos 错误格式不同。客户端按状态码处理即可。
- 本地经 Docker Desktop 端口转发访问时，所有请求的来源 IP 都是 Docker 网络的网关地址，限流对本机流量等于全局限流。初始值按测试流量留了余量。

**客户端 IP**：

- APISIX 默认追加 `X-Forwarded-For`，再转发给上游。
- account 的 `security.trusted_proxies` 改为 `${TRUSTED_PROXY:}`：容器内设为 APISIX 的固定 IP，宿主机模式为空。
- `pkg/clientip` 从右往左跳过可信代理，第一个不可信的地址就是客户端 IP，按 IP 限流因此在网关后面仍然有效。

**TLS**：

- `make certs` 生成 `localhost` 与 `127.0.0.1` 的自签证书，放在 `deploy/apisix/certs/`。这个目录被 git 忽略，私钥不提交。
- Standalone 配置中的 SSL 对象需要 PEM 内容。第一阶段先验证用 `$env://` 引用环境变量；不支持时，`apisix.yaml` 改为由 `make certs` 从模板生成（模板提交，生成结果被忽略）。
- 19080 的 HTTP 保留，测试用 HTTP；19443 用于演示 HTTPS 和 WSS。不做 HTTP 到 HTTPS 的跳转。

### 3.4 多实例相关的改动

- **端点顺序**：account、social、message、access 的 `kratos.Server` 改为 HTTP 服务器在前（2.3 节）。
- **access 的路由地址**：
  - 配置 `advertise_addr` 默认改为空。为空时，在 Kratos 的 `BeforeStart` 钩子里，从 `kratos.FromContext(ctx).Endpoint()` 取已注册的 `grpc://` 端点，设置到 Hub；
  - Kratos 在执行钩子之前就算好了端点，HTTP 服务器在钩子之后才启动，所以连接进来时地址一定已经就绪；
  - 这样不需要让 Hub 依赖 gRPC 服务器，不会造成 wire 依赖成环。路由中的地址与 Consul 中登记的地址一致；
  - 配置非空时仍以配置为准，用于 NAT 等特殊网络。
- **message 多实例**：
  - Outbox 发布器靠 Redis 租约保证只有一个实例在发布；
  - Kafka 消费组在两个实例之间分摊分区；
  - 节点号由 Redis 租约分配，多实例不冲突。
- **delivery 多实例**：
  - 消费组分摊分区，`delivery:done:{event_id}` 去重在实例之间共享。事件 ID 由全局唯一的节点号生成，在各服务之间也不重复。
  - 消费者的会话超时新增为配置 `kafka.session_timeout`，默认 10 秒；franz-go 自身默认 45 秒。
  - 实例被杀时，分区要等会话超时后才会转给其他实例，这段时间推送会延迟，但不会丢：位点没有提交，接手的实例会从上次提交的位置继续处理。
  - 正常停机时消费者会主动离开消费组，不需要等会话超时。
- **Outbox 租约（TD-32）**：
  - 发布器记录本地认定的租约截止时间，计算方式与 `pkg/idgen` 相同：发出续期请求前的时间加上 TTL；
  - 每批 `ProduceSync` 之前检查剩余租期，不足一次发布超时（5 秒）加 1 秒余量时，先同步续期，续期失败就停止发布；
  - 正常情况下每 3 秒续期一次，剩余租期总在 7 秒以上，这个检查只在长时间停顿之后才会触发；
  - 检查之后、发布之前如果再出现长时间停顿，仍可能短暂出现两个发布者。消费方按事件 ID 幂等，重复可以容忍，残留风险写进技术债的处理记录。

### 3.5 服务间调用：超时、重试、熔断

**超时**：

- 现有按上游配置的超时不变：message 调 social、account 1 秒，access 调 message 3 秒，delivery 推送 2 秒。
- Kratos 的 gRPC 客户端会把截止时间随调用传给下游。

**重试**：新增 `pkg/middleware/retry`，作为 Kratos 客户端中间件：

- 只重试一次，即最多 2 次尝试；两次之间等待 20 到 50 毫秒的随机时间。
- 只在以下情况重试：
  - gRPC 状态为 `Unavailable` 且错误不带 Kratos 原因，即连接级失败，如实例已退出或连接被拒绝；
  - 剩余截止时间大于 100 毫秒。
- 不重试的情况：
  - 熔断打开（原因 `CIRCUITBREAKER`）；
  - 业务错误；
  - 下游返回的 `DEPENDENCY_UNAVAILABLE`：下游的依赖不可用时，换实例重试只会放大压力。
- 只用于幂等调用，按客户端在代码中列出允许重试的方法：
  - message 调 social：`CheckSendPermission`、`ListAllGroupMembers`、`BatchGetGroups`；
  - message 调 account：只读接口；
  - delivery 调 social：`ListAllGroupMembers`；
  - access 调 message：`SendMessage`。按 `client_msg_id` 幂等，重复执行只存一条，返回同一个 seq。
- 不重试推送：`Push` 有时效性，重复推送也没有意义，丢了由客户端补拉。
- grpc-go 已经会透明重试“请求还没发出去”的失败，这个中间件补上的是实例在处理中途退出、或者选中了已失效连接的情况。重试时再经过 Kratos 的选择器，它只在 READY 状态的连接中选择（项目用的是 wrr），实例退出后它的连接很快离开 READY，所以重试通常落到另一个实例。

**熔断**：

- 用 Kratos 自带的 `middleware/circuitbreaker` 客户端中间件。它实现 Google SRE 的自适应限流，按接口统计，不需要新依赖。
- 它把 500、503、504 计为失败，打开时直接返回 `503 CIRCUITBREAKER`，请求不会发出。
- 接入的调用：message 调 social 和 account，access 调 message，delivery 调 social。
- 中间件顺序为重试在外、熔断在内：每次尝试都计入熔断统计，熔断打开时不再重试。
- access 把 `CIRCUITBREAKER` 原样透传给客户端。这表示消息确定没有发出，可以稍后重发，与 `UPSTREAM_UNAVAILABLE`（结果未知）不同，写进 data-flow.md。
- delivery 调 access 的推送**不接熔断**。熔断按接口统计，一个 access 实例宕机会让发往所有实例的推送一起被熔断。单个实例的故障由 3.7 节的冷却处理。

### 3.6 Kafka 消费：死信队列与重试队列（TD-30）

现在的问题有两个：

- 一条事件持续失败但没有被标记为 `Permanent` 时，会阻塞整个分区；
- 解码失败和 panic 只记日志就跳过，原始记录没有留下来。

两类消费者对失败的要求不同：

| 消费组 | 顺序要求 | 典型失败 | 期望行为 |
|---|---|---|---|
| `marschat-message`（群会话成员投影） | 同一个群的事件必须按顺序处理 | MySQL 不可用 | 原地等待，不能跳过，否则投影错乱 |
| `marschat-social-cache`（删除群缓存） | 无（删除操作与顺序无关） | Redis 不可用 | 不应阻塞后面的事件 |
| `marschat-delivery`（推送、通知） | 无 | 处理器尽力推送，失败也返回 nil | 基本不会失败 |

方案（第 8 节问题 3 的推荐方案 A）：

- **死信队列**：新增 topic `marschat.dlq`，3 个分区，保留 14 天，所有消费组共用。
  - 以下记录写入死信队列后再提交位点：`Permanent` 错误、解码失败、panic，以及在重试队列中被判为不可重试的记录。
  - 写入内容为原始的值和头部，另加头部 `x-dlq-group`、`x-dlq-topic`、`x-dlq-partition`、`x-dlq-offset`、`x-dlq-error`。
  - 写入死信队列失败时按临时错误处理，原地重试，不提交位点，保证记录不会凭空消失。
- **有序消费者**（`ConsumerConfig.Ordered = true`，用于 `marschat-message`）：
  - 临时错误照旧原地无限重试；
  - 连续失败 10 次之后升级为 ERROR 日志，并限制输出频率，让分区阻塞能被发现。步骤 8 按消费积压告警。
- **无序消费者**（用于 `marschat-social-cache`）：
  - 原地重试 3 次仍失败，就写入本组的重试 topic `marschat.<组名>.retry`，例如 `marschat.social-cache.retry`，3 个分区，再提交原位点。主分区因此不被阻塞。
  - 重试消费者是同一个 `Consumer` 类型，消费组为 `<组名>-retry`，使用相同的处理器。每条记录带头部 `x-retry-not-before`；没到时间就等待，等待可以被停机打断。
  - 所有记录的延迟都是固定的 10 秒，写入顺序就是到期顺序，等待一条记录不会挡住已经到期的记录。
  - 重试消费者遇到临时错误时原地无限重试，只阻塞重试 topic；遇到 `Permanent` 错误才写入死信队列。这样 Redis 故障期间不会把大量正常事件送进死信队列。
- 重试与死信的参数（3 次、10 秒）写成 `ConsumerConfig` 的字段，由各服务在代码中设置。它们不是限额，不做热更新。
- 死信只提供查看方式，在 deployment.md 写明用 `kafka-console-consumer.sh` 带头部读取的命令；重新投递工具以后需要时再做。
- 这个机制对 `marschat-social-cache` 的实际收益有限：它的失败几乎都来自 Redis 整体不可用，而不是某条记录本身有问题。选择在这里落地，是为了先把机制做好，等到有逐条失败的消费者时直接复用，例如步骤 6 的缩略图处理。第 8 节问题 3 给了只做死信队列的方案 B。

**节点号租约恢复（TD-34，新增）**：

- `pkg/idgen` 续期时如果发现键不存在（不是被其他实例占用），先等待本地截止时间过去，再重新走一遍获取流程，可能拿到新的节点号，之后恢复发号。
- 键被其他实例占用时同样重新获取。
- 重新获取之前，发号仍返回 `ErrLeaseLost`。截止时间之后旧节点号上不会再生成 ID，换节点号后生成的 ID 不会与其他实例重复。

### 3.7 delivery：access 连接管理与 social 实时通知

**推送失败的处理（TD-31）**：

- 推送返回 `Unavailable` 或 `DeadlineExceeded` 时，关闭并移出缓存的 gRPC 连接，这个地址进入冷却期。新增配置 `push.cooldown`，默认 5 秒，可热更新（实施时改为 `policy.push_cooldown`，见第 9 节）。
- 冷却期内发往这个地址的推送直接跳过，记 DEBUG 日志；冷却结束后，下一次推送重新拨号。
- 效果：
  - access 实例宕机后，每个 delivery 实例每 5 秒最多对它付出一次 2 秒的超时，而不是每条事件都要等 2 秒；
  - 失效地址的连接在第一次失败时就移出缓存，缓存不会只增不减。
- 不监听 Consul 中 access 实例的变化。那样做需要处理“正在排空的实例已从 Consul 注销但仍有连接”的情况，按失败处理更简单，也不依赖注册地址与路由地址严格一致。

**social 实时通知**：

- delivery 的消费组 `marschat-delivery` 增加订阅 `marschat.social.events`。新 topic 没有提交过的位点，从最新位置开始。
- 收到事件后查找在线路由并推送。推送尽力而为，客户端重连后通过列表接口刷新。
- 推送对象：

| 事件 | 推送对象 |
|---|---|
| `FriendRequestCreated` | `to_user_id` |
| `FriendAdded`、`FriendDeleted` | `user_id` 与 `friend_id` |
| `GroupCreated` | 事件中的 `member_ids` |
| `GroupMembersJoined`、`GroupMemberUpdated`、`GroupUpdated` | 群的当前全部成员（经 `ListAllGroupMembers`） |
| `GroupMemberLeft` | 群的当前全部成员，加上离开的 `user_id` |
| `GroupDismissed` | 事件中的 `member_ids`（群已解散，查不到成员） |
| `GroupJoinRequestCreated` | 群主和管理员（按 `ListAllGroupMembers` 返回的角色筛选） |

- 帧协议新增 `ServerFrame.notification`，接口变化见第 4 节。
- 事件不带发起连接的 ID，操作者自己的连接也会收到通知。客户端按 `event_id` 去重或直接刷新即可。
- 去重沿用 `delivery:done:{event_id}`。

### 3.8 access 优雅停机

收到 SIGTERM 后的顺序：

1. Kratos 从 Consul 注销。APISIX 的 Consul 长轮询约 1 秒内更新上游，新的 WebSocket 不再分配到这个实例。
2. Kratos 并发停止各服务器：
   - **HTTP 服务器**：`Shutdown` 立刻关闭监听。已升级的 WebSocket 连接不受影响；新的握手会被拒绝连接，APISIX 按连接失败换到另一个实例。
   - **Hub**：进入排空状态，把现有连接分批关闭，均匀分布在 `websocket.drain_window` 内（新增配置，默认 5 秒），避免全部客户端同时重连到剩下的实例。每条连接发送关闭码 1001，原因为 `server shutting down`，并等待对端回应关闭帧，沿用现有的 2 秒宽限。连接关闭时照旧删除在线路由。
   - **gRPC 服务器**：用一个包装类型（嵌入 Kratos 的 gRPC 服务器，只重写 `Stop`），先等 Hub 排空完成或停机超时，再执行 `GracefulStop`。所以在分批关闭期间，发往尚未关闭的连接的推送仍然有效。
3. `kratos.StopTimeout` 设为 15 秒，Compose 的 `stop_grace_period` 设为 20 秒，留出排空和收尾的时间。

客户端约定，写进 data-flow.md：

- 收到 1001 或连接异常断开后，随机等待 0 到 2 秒再重连；
- 重连认证成功后，对每个会话按本地最后的 seq 调用 `GetDifference` 补齐；
- 补拉与推送可能重叠，按 `(conversation_id, seq)` 去重。

没有采用的做法：在 `BeforeStop` 里自己先注销、等待、关连接，再交给 Kratos 停机。Kratos 随后还会再注销一次，Consul 对不存在的服务 ID 返回错误，Kratos 的 `Stop` 遇到注销错误会直接返回，不再取消上下文，服务器就不会停止。

### 3.9 故障实验

测试放在 `test/cluster`，构建标签为 `cluster`，用 `make cluster-test` 运行，前提是先执行 `make cluster-up`。测试的访问方式：

- 业务请求只经网关：`http://127.0.0.1:19080` 和 `ws://127.0.0.1:19080/ws`；
- 核对状态时直接读 Redis 的宿主机端口，例如按在线路由判断一条连接落在哪个 access 实例；
- 注入故障时调用 `docker` 命令。辅助函数只接受 `marschat-` 前缀的容器名，不会碰到其他项目。

`test/e2e` 中的 WebSocket、HTTP 客户端辅助函数挪到普通包 `test/testkit`，两组测试共用。

每个实验都要验证不丢、不重：

- 测试客户端按会话记录经推送和补拉收到的 seq，最终集合必须恰好是 1..N；
- 历史接口中消息条数为 N，每个 `client_msg_id` 只出现一次。

| 实验 | 做法 | 预期 | 记录 |
|---|---|---|---|
| 跨节点收发 | A、B 分别连到两个 access 实例（反复连接，直到在线路由显示落在不同实例），互发消息 | 推送实时到达，seq 连续 | 推送延迟 |
| 杀掉 access | 持续发消息时 `docker kill` 一个 access | 该实例上的客户端断开（1006），经网关重连到另一实例，补拉后不丢不重 | 重连耗时、补拉条数 |
| 滚动重启 access | 依次 `docker restart -t 20` 两个 access，等前一个在 Consul 中恢复 passing 后再重启下一个 | 客户端收到 1001 而不是 1006；发送请求全部得到确认；不丢不重 | 每次重启的断线时长 |
| 杀掉 delivery | `docker kill` 一个 delivery | 会话超时（10 秒）后分区转给另一个实例，推送恢复；断线期间的消息延迟到达或经补拉拿到 | 推送中断时长 |
| 杀掉持有 Outbox 租约的 message | 从 Redis 读租约持有者，杀掉对应容器 | 另一个实例在租约 TTL（10 秒）内接手，事件继续发布；可能有重复，被消费幂等吸收 | 接手耗时、重复事件数 |
| 暂停 Kafka | `docker pause` Kafka 30 秒 | 发送仍然成功（事件写入 Outbox），推送暂停；恢复后积压的事件按顺序发布并推送 | 恢复后的追平时间 |
| 停掉 Redis | `docker stop` Redis 20 秒；修复 TD-34 后再停 60 秒 | 见下面的说明 | 各项降级的实际表现 |

停掉 Redis 时的预期：

- 20 秒以内：
  - 发送成功（限流放行、令牌吊销检查放行）；
  - 在线路由写入失败只记 WARN，推送停止（查不到路由、去重失败都不推送）；
  - Outbox 拿不到租约，暂停发布。
- 恢复后：发布继续，客户端靠补拉拿到这段时间的消息。
- 60 秒：节点号租约超时后发号失败，写操作返回错误；Redis 恢复后自动重新获取节点号，写操作恢复。这一项验证 TD-34 的修复。

## 4. 接口、配置与数据变化

**帧协议**（`api/access/v1/frame.proto`，接口协议变化，同步更新 data-flow.md）：

```proto
message ServerFrame {
  // 现有字段 1～8 不变
  Notification notification = 9;
}

// Notification 是 social 事件的实时通知，尽力推送；客户端重连后应通过列表接口刷新。
message Notification {
  // 事件 ID，可用于去重
  string event_id = 1;
  oneof body {
    social.v1.FriendRequestCreated friend_request_created = 2;
    social.v1.FriendAdded friend_added = 3;
    social.v1.FriendDeleted friend_deleted = 4;
    social.v1.GroupCreated group_created = 5;
    social.v1.GroupMembersJoined group_members_joined = 6;
    social.v1.GroupMemberLeft group_member_left = 7;
    social.v1.GroupMemberUpdated group_member_updated = 8;
    social.v1.GroupUpdated group_updated = 9;
    social.v1.GroupDismissed group_dismissed = 10;
    social.v1.GroupJoinRequestCreated group_join_request_created = 11;
  }
}
```

**配置**：

| 配置 | 位置 | 默认 | 说明 |
|---|---|---|---|
| `registry.consul.check_interval` | `pkg/conf.Registry.Consul` | 10s | TCP 检查间隔，TTL 为 2 倍；容器内为 3s |
| `registry.consul.health_check` | 各服务配置 | `${CONSUL_HEALTH_CHECK:false}` | 容器内为 true |
| `kafka.session_timeout` | `pkg/conf.Kafka` | 10s | 消费组会话超时 |
| `websocket.drain_window` | access | 5s | 停机时分批关闭连接的时间窗口 |
| `advertise_addr` | access | 空 | 为空时取已注册的 gRPC 端点 |
| `push.cooldown`（实施为 `policy.push_cooldown`） | delivery | 5s | 推送失败后的冷却期，可热更新 |
| `security.trusted_proxies` | account | `${TRUSTED_PROXY:}` | 容器内为 APISIX 的 IP |

- Kafka topic：新增 `marschat.dlq`（3 个分区，`retention.ms` 为 14 天）、`marschat.social-cache.retry`（3 个分区），由 `kafka-init` 创建。
- Redis 键：没有新增。冷却状态保存在 delivery 进程内。
- 数据库：不变。

**Compose 与 Makefile**：

- `compose.yaml`：
  - 固定子网；
  - 新增 `apisix` 和五个应用服务，都放在 profile `apps` 中；
  - Kafka 设置 `KAFKA_HEAP_OPTS=-Xmx512m -Xms512m`；
  - `kafka-init` 增加新 topic。
- 新增的 make 目标：
  - `make images`：编译并打包镜像；
  - `make certs`：生成网关证书；
  - `make cluster-up`、`make cluster-down`：启停容器模式，`cluster-down` 只停 profile `apps` 的容器；
  - `make cluster-test`：运行故障实验。
- `.gitignore`：新增 `build/`、`deploy/apisix/certs/`；如果采用模板生成，还要加上生成的 `apisix.yaml`。

## 5. 候选方案与权衡

| 问题 | 采用 | 备选 | 理由 |
|---|---|---|---|
| 网关的服务发现 | APISIX Consul 发现 | Docker 内置 DNS；静态节点 | 与服务间调用共用同一个注册中心，实例上下线自动生效。DNS 作为实验失败时的退路 |
| APISIX 配置方式 | Standalone，读 YAML 文件 | etcd 加 Admin API；API 驱动的 Standalone | 不多跑一个 etcd，配置随仓库管理、改动热生效。API 驱动的方式是给 Ingress Controller 用的 |
| 镜像构建 | 宿主机交叉编译加最小运行镜像 | 多阶段 Dockerfile | 虚拟机内存紧张，可以复用宿主机的模块缓存和代理。CI 需要时再补 |
| 运行镜像 | distroless static nonroot | alpine；scratch | 只有二进制、CA 证书和非 root 用户。alpine 有 shell，方便排查；scratch 没有 CA 证书和时区数据 |
| 网关的信任方式 | 固定网关 IP | 信任整个子网 | 范围最小。网络内的其他容器不能伪造 `X-Forwarded-For` |
| 重试 | 自己实现的客户端中间件，配方法白名单 | gRPC 服务配置中的 `retryPolicy`；不重试 | Kratos v3 没有重试中间件。gRPC 的服务配置会和 Kratos 设置负载均衡策略的服务配置冲突，也不好按 Kratos 错误原因判断 |
| 熔断 | Kratos `circuitbreaker` 中间件 | sony/gobreaker | 不增加依赖。SRE 自适应限流不用调阈值 |
| TD-31 | 失败即摘除并冷却 | 监听 Consul 中 access 的变化 | 代码少，同时解决“宕机实例每次都要等超时”和“缓存只增不减”两个问题 |
| TD-30 | 死信队列，加上仅限无序消费者的重试队列 | 只做死信队列；所有消费者都用重试队列 | 重试队列会打乱顺序，不能用于群成员投影。重试消费者原地重试临时错误，故障期间不会把正常事件送进死信队列 |
| TD-32 | 发布前检查剩余租期 | 记录中带租约代数，消费方丢弃旧代数的记录 | 代数方案要求每个消费方按键保存最新代数，改动面大；现有的消费幂等已经能吸收重复 |
| access 停机 | 包装 gRPC 服务器，等 Hub 排空后再停 | 在 `BeforeStop` 中自己注销 | 后者会导致 Kratos 第二次注销失败而不停机（3.8 节） |

## 6. 风险与验证

| 风险 | 验证或应对 |
|---|---|
| Standalone 与 Consul 发现的组合没有官方示例 | 第一阶段先做实验：两实例分流，杀掉一个后被摘除。不通过就改用 DNS，并在 ADR-0005 的风险一节记录结果 |
| 调整端点顺序后，Kratos 的 gRPC 发现是否受影响 | 宿主机模式下运行 `make e2e`（经发现调用 gRPC），并在 Consul 中核对 `Address:Port` 与 `TaggedAddresses` |
| Docker Desktop 的目录挂载可能不把文件变化通知传进容器，限额热更新在容器内失效 | 实验验证。失效时，容器模式下改配置后重启对应容器，并在 deployment.md 写明；宿主机模式的热更新不受影响 |
| 内存不足 | 限制 Kafka 堆内存，应用容器设置 `mem_limit`；第一阶段用 `docker stats` 实测并记录 |
| 镜像标签 | `apache/apisix:3.20.0` 的变体和 distroless 的标签在实施时于本地拉取确认，锁定具体标签，不用 `latest` |
| 本地所有请求来自同一个 IP，网关限流对测试是全局的 | 初始值留余量；限流的效果用单独的测试验证（超限返回 429） |
| `kafka.session_timeout` 太短会让 GC 停顿或网络抖动触发不必要的再平衡 | 10 秒高于 broker 的下限（6 秒），故障实验中观察是否有异常的再平衡 |
| 优雅停机时剩下的实例要承接全部连接 | 用 `drain_window` 分批关闭；重启回来的实例只接新连接，连接分布不会自动回到均衡，记为已知限制 |
| 自签证书 | 只用于本地演示，测试经 HTTP 进行；HTTPS 和 WSS 只做冒烟验证 |

## 7. 实施顺序

1. **容器模式**：
   - 端点顺序、`check_interval`、`health_check` 环境变量、access 地址自动获取；
   - `make images`、Compose 的 `apps` profile、固定子网、Kafka 堆内存限制；
   - 验证：Consul 中是容器 IP、HTTP 端口在前；跨 access 实例推送；`make e2e` 在宿主机模式下仍然通过；`docker stats` 记录内存。
2. **网关**：
   - Standalone 与 Consul 的组合实验，决定是否继续；
   - 路由、WebSocket、限流、可信代理、TLS；
   - 网关冒烟测试。
3. **调用韧性**：重试中间件、熔断、TD-32、TD-31、TD-34、access 优雅停机，并补单元测试。
4. **消费韧性与通知**：死信队列与重试队列（TD-30），social 实时通知（proto、delivery）。
5. **故障实验**：
   - `test/testkit` 抽取、`test/cluster`，运行实验并把结果记入计划；
   - 更新文档：deployment、system、data-flow、features、tech-debt、ADR-0005 风险一节，以及计划中“生产环境技术点”表里 Outbox 一行（`FOR UPDATE SKIP LOCKED` 改为 Redis 租约）。

每段完成后按 `buf lint` → `go vet` → `go build` → `go test -race` 验证，改动 proto 或 wire 后执行 `make gen`。第 1、2 段完成后还要运行 `make e2e`。

## 8. 待确认问题

1. **镜像构建与运行镜像**：推荐宿主机交叉编译，运行镜像用 distroless static nonroot。备选是用 alpine 作为运行镜像，可以进容器排查，代价是镜像稍大。两种方式都需要拉取新镜像（运行镜像与 `apache/apisix:3.20.0`），确认后在实施时拉取。
2. **Compose 网络与资源**：
   - 网络改为固定子网 `172.30.0.0/24`，APISIX 固定 IP `172.30.0.10`。需要先 `docker compose down`（保留数据卷），再重建 MarsChat 的四个依赖容器；
   - Kafka 堆内存限制为 512 MiB。
3. **TD-30 的范围**：
   - 推荐方案 A：死信队列加上仅限无序消费者的重试队列（3.6 节）；
   - 方案 B：只做死信队列，重试队列等到有逐条失败的消费者（例如步骤 6）时再加。B 更简单，但计划中“重试队列与死信队列”这一项要拆开，本步骤只完成一半。
4. **TD-26（服务间认证）**：推荐本步骤不做。理由：
   - 内部 gRPC 端口不发布到宿主机，网关只转发 HTTP 路由；
   - Compose 网络中只有 MarsChat 自己的容器。

   留到步骤 9 或可选扩展中评估 mTLS。如果希望本步骤处理，最简单的做法是共享服务令牌：用 HMAC 签名放进 gRPC 元数据，并配合密钥轮换。
5. **social 实时通知**：推荐随本步骤实现（3.7 节、第 4 节），备选是推迟到前端重构时再做。

## 9. 实施说明

实施中与上文的差异和决定，随各段完成补充。

| 段 | 项 | 实施 | 原因 |
|---|---|---|---|
| 1 | Consul 检查配置 | 不新增 `check_interval`，而是用时长字段 `registry.consul.tcp_check_interval` 取代布尔字段 `health_check`（字段号 2 保留）：`0s` 表示不做 TCP 探测，大于 0 时开启 TCP 探测，并作为 TTL 心跳间隔 | `${CONSUL_HEALTH_CHECK:false}` 替换后是字符串 `"false"`，protojson 拒绝（`invalid value for bool field healthCheck`）。打开 Kratos 的 `WithResolveActualTypes` 会把 `000000` 这类纯数字字符串也转成数字，不可取。时长字段在配置中本来就是字符串，可以直接用占位符覆盖 |
| 1 | access 路由地址 | `data.RouteRepo` 改为导出类型，`ResolveAddr` 作为 `kratos.BeforeStart` 钩子，在 wire 中用 `wire.Bind` 绑定到 `biz.RouteRepo` | 与 3.4 节一致；main 需要拿到具体类型才能注册钩子 |
| 1 | 运行镜像 | `gcr.io/distroless/static-debian13:nonroot`，按 digest 锁定 | distroless 没有带版本号的标签 |
| 1 | APISIX 镜像 | `apache/apisix:3.20.0-debian`，镜像 638 MB | 本地拉取确认；Docker Hub 连接不稳定，重试三次才拉下来 |
| 2 | Consul 发现的超时 | `timeout.read` 35000 毫秒、`timeout.wait` 30 秒 | 阻塞查询要等 `wait` 秒才返回，默认的 `read` 2000 毫秒小于默认的 `wait` 60 秒，每次长轮询都会读超时 |
| 2 | 插件与 worker | `stream_plugins: []`；`nginx_config.worker_processes: 2` | 只列出 HTTP 插件时，默认 stream 插件列表中的 `syslog` 依赖 prometheus 的共享内存，加载报错；worker 默认按 CPU 核数（本机 24 个） |
| 2 | TLS 证书注入 | `$env://` 方案可行：compose 覆盖入口命令，把挂载的证书文件读入 `MARSCHAT_TLS_CERT`、`MARSCHAT_TLS_KEY` 后执行镜像原来的 `docker-entrypoint.sh docker-start`；`config.yaml` 的 `nginx_config.envs` 放行这两个变量；证书由 `tools/devcert`（`make certs`）生成 | 不需要模板生成 `apisix.yaml`；APISIX 的 SSL 对象支持 `$env://` 引用（`apisix/secret.lua`） |
| 2 | 本机经网关的客户端 IP | `172.30.0.128` | Docker 把 `ip_range` 的第一个地址作为网络网关，本机经端口转发的请求以它为来源 |
| 2 | 测试辅助函数 | 抽到 `test/testkit`（原计划在第 5 段） | 第 2 段就需要经网关的 WebSocket 客户端来验证跨实例推送 |
| 3 | Outbox 跨实例唤醒 | `Notify` 在本实例不持有租约时，向 Redis 频道 `outbox:wake:{source}` 发布一条消息，持有租约的实例订阅该频道，收到后立即发布；发布失败只记 DEBUG，退回轮询 | 第 2 段发现的延迟问题。频道按事件来源区分，只有持有者订阅才会处理，不持有租约时不发本地以外的唤醒 |
| 3 | TD-32 | 没有改代码，只更新技术债描述，保留在“低” | 核对后发现“续期到期就先续期，剩余租期不足一次发布超时（5 秒）就停止发布”在步骤 4 已经实现，只是没有 1 秒余量（本地截止时间按发出续期请求前的时间计算，本身偏保守）。3.4 节“消费方按事件 ID 幂等，重复可以容忍”不准确：message 的群成员投影没有按事件 ID 去重，迟到的重复入群事件会把已退群的成员加回来。触发条件是进程在发布途中停顿超过 5 秒，见 TD-32 |
| 3 | 推送冷却的配置位置 | 放在 delivery 新增的 `policy` 段（`policy.push_cooldown`，热更新），而不是 `push` 段 | 冷却期是可调的运行参数，按项目约定放在热更新的 `policy` 段；`push` 段（超时）修改后需要重启 |
| 3 | TD-34 的处理方式 | 续期发现租约丢失时立即改用新的空闲节点号，不等旧租约的本地截止时间 | 新节点号上的 ID 不会与旧节点号冲突，不需要等待；获取失败时每秒重试 |
| 3 | 重试的剩余时间要求 | 剩余截止时间不少于 150 毫秒（100 毫秒加最大退避 50 毫秒）才重试 | 退避本身也占用截止时间 |
| 3 | 分批关闭的节奏 | 每 100 毫秒关闭一批，批大小为连接数除以窗口内的批数（向上取整）；连接少时一批一个、提前结束 | 关闭速率不超过“连接数 / 窗口”，连接少时不必拖满整个窗口 |
| 4 | 有序与无序的区分 | 没有新增 `ConsumerConfig.Ordered`：没有配置 `RetryTopic` 的消费者就是原地无限重试（有序），配置了才转入重试队列 | 两个字段表达的是同一件事，只留一个，不会出现“标为有序却配了重试队列”的组合 |
| 4 | 重试 topic 的名字 | 由服务在代码中显式给出（`kafka.TopicSocialCacheRetry`），不按组名拼接 | 名字与 `kafka-init` 中创建的 topic 一一对应，便于查找 |
| 4 | 转发时的记录头 | 重试记录额外带 `x-retry-from`（原记录的 topic/partition/offset）；转发或写入死信队列时去掉上一次追加的 `x-retry-*`、`x-dlq-*` | 重试后再进死信队列时，仍能找到最初的位置 |
| 4 | 升级为 ERROR 的日志 | 对所有原地重试生效（不只有序消费者）：同一条记录连续失败 10 次后记 ERROR，之后每分钟最多一条 | 无序消费者写不进重试队列时同样会长时间原地重试 |
| 4 | 通知的接收用户 | 入群、退群、成员变化在“群的当前成员”之外，再并上事件中的当事人（新成员、离开的人、被修改的成员），并去重 | 成员列表缓存还没刷新时，当事人也能收到；重复的用户会让同一连接收到两次推送 |
| 4 | e2e 专用重试 topic | `kafka-init` 增加 `marschat.e2e.retry` | `kafka_test.go` 经真实 Kafka 验证重试队列往返，不使用业务的重试 topic |
| 5 | Redis 客户端的拨号保护 | `pkg/bootstrap.NewRedis` 加拨号钩子：拨号失败后标记不可达，期间的拨号直接返回上次的错误，后台立即探测、之后每秒一次，成功后恢复；`DialerRetries` 设为 1 | 故障实验发现停掉 Redis 约 3 秒后发送开始失败。容器停止后服务名从 Docker DNS 中消失，每次解析约 1 秒才失败，go-redis 默认单次拨号重试 5 次、命令重试 3 次，一条命令就用完了调用方的截止时间，限流的“失败放行”来不及生效。见 [lessons/redis-dial-stall-docker-dns.md](../lessons/redis-dial-stall-docker-dns.md) |
| 5 | 60 秒 Redis 实验的预期 | 分别验证两类写操作：已有会话的发送全程成功；给新好友发第一条消息（要为新会话发号）在租约到期后失败，Redis 恢复后成功 | 3.9 节写的“发号失败，写操作返回错误”不准确：message 只有新建单聊会话时才发号 |
| 5 | 测试客户端 | `test/cluster/session_test.go` 实现客户端约定（断线后随机等待 0～2 秒重连、从本地连续 seq 补拉、按 seq 去重），按连接记录推送、补拉、断线和关闭码；故障注入只操作 `marschat-` 前缀的容器，结束时（包括失败）恢复 | 实验要验证的是“按约定实现的客户端不丢不重”，不能只看服务端日志 |
| 5 | 发号失败的错误码 | 保持 `500 INTERNAL`，记为 TD-35 | 映射为 503 涉及 account、social、message 三个服务的错误处理，留到步骤 8 接入告警时处理（2026-11-20 随邮箱验证码一起解决，改为 `503 DEPENDENCY_UNAVAILABLE`） |

第 1 段验证（2026-11-19）：

- `make images` 构建五个镜像（各 50～58 MB）；`docker compose down` 后按固定子网重建依赖容器，数据卷保留；`make cluster-up` 后 8 个应用容器无重启。
- Consul：各实例登记容器 IP（`172.30.0.128/25` 段），`Address:Port` 为 HTTP 端口，`TaggedAddresses` 含 `grpc`、`http`；两个端口的 TCP 检查和 TTL 检查均为 passing。
- access 两个实例的路由地址分别解析为各自容器 IP 的 19100 端口；Outbox 租约只有一个 message 实例持有；`marschat-delivery` 的 15 个分区按 8/7 分给两个实例，`marschat-message` 的 6 个分区按 3/3 分配。
- `make cluster-down` 停止时各实例主动从 Consul 注销（目录为空）。
- 宿主机模式回归：五个服务在宿主机运行，account 登记为 `127.0.0.1:18010`（HTTP 在前）、只有 TTL 检查；access 路由地址解析为 `127.0.0.1:19100`；`make e2e` 全部通过，日志无 ERROR。
- 内存：应用容器各 15～56 MiB，Kafka 512 MiB，MarsChat 合计约 1.2 GiB（网关未计入）。
- 跨 access 实例推送要经网关才能从宿主机测试，移到第 2 段之后验证。

第 2 段验证（2026-11-19）：

- Standalone 与 Consul 组合实验通过：两个 message 实例按轮询各处理一半请求；`docker kill` 一个实例后每 0.1 秒一次、持续 15 秒的请求全部成功（150 次），其中两次因 2 秒连接超时后由网关换节点重试而耗时约 2 秒，约 5 秒后不再转发到该实例；`docker start` 后约 2.5 秒恢复 passing，重新分到一半流量。
- `apisix.yaml` 经目录挂载热更新有效（把限流临时调到每秒 1 次，0.5 秒内生效并返回 429，改回后恢复）；服务配置的 `policy` 在容器内同样热更新有效。
- `make cluster-test`（`test/cluster`，5 项）通过：网关路由与 404、HTTPS 与 WSS、伪造的 `X-Forwarded-For` 被忽略、跨 access 实例推送与跨实例多端同步、300 个并发请求中 196 个被限流。网关空载内存 55 MiB。
- testkit 改造后宿主机模式 `make e2e` 30 项通过，日志无 ERROR。

第 2 段发现的问题：跨实例推送延迟呈两极分布，发送请求落在持有 Outbox 租约的实例上时为 2～3 ms，落在另一个实例上时为 95～500 ms。事务提交后的立即唤醒只在本进程内有效，另一个实例要等持有租约的实例下一轮轮询（0.5 秒）。第 3 段在 `pkg/outbox` 中加入经 Redis 发布订阅的跨实例唤醒。

第 3 段验证（2026-11-19，单元测试）：

- `pkg/middleware/retry`：连接级失败重试一次后成功；最多两次尝试；方法不在白名单、业务错误、超时、`DEPENDENCY_UNAVAILABLE`、`CIRCUITBREAKER`、`no_available_node`、剩余时间不足都不重试。
- `pkg/outbox`：非持有者的 `Notify` 经 Redis 唤醒持有者；持有者的 `Notify` 只在本地唤醒。变异检查确认去掉跨实例唤醒后用例失败。
- `pkg/idgen`：键被占用、键过期后改用新节点号；续期出错时在本地截止时间前继续使用旧节点号。
- delivery `pusher`：不可达的实例被摘除并进入冷却，冷却期内直接跳过，冷却结束后重新拨号；业务错误不摘除；冷却期为 0 时只摘除。
- access：连接在 `drain_window` 内分批关闭（20 个连接、1 秒窗口时，第一批立即关闭，最后一批在 700 毫秒之后），变异检查确认一次全部关闭时用例失败；停机超时到期时剩余连接立即断开；`DrainingGRPCServer` 在 Hub 排空前继续处理请求，排空或超时后停止。
- `buf lint`、`go vet ./...`、`go build ./...`、`go test -race ./...` 通过。
- 重建镜像后 `make cluster-test` 6 项通过，新增 `TestCrossNodePushLatency`：10 条跨 access 实例的消息从发送到对端收到推送为 8～14 ms（修复前约一半要 95～500 ms）；这 10 条加上前一项的 2 条按 6/6 分到两个 message 实例，其中只有一个持有租约，另一个上的 6 条都经过跨实例唤醒。各容器日志无 ERROR。
- 滚动重启、实例被杀等场景放到第 5 段的故障实验。

第 4 段验证（2026-11-20）：

- `pkg/kafka` 单元测试 17 项：`Permanent`、panic、解码失败写入死信队列并带齐头部；死信写入失败时重试到成功、停机时不提交；原地 3 次后转入重试队列（最早处理时间与原位置正确）、写不进重试队列时留在原地；重试消费者等到时间才处理、等待有上限、可被停机打断，临时错误原地重试、`Permanent` 进死信队列；连续失败 10 次后只记一条 ERROR。
- delivery 单元测试：10 种 social 事件的接收用户、重复事件只推一次、查不到成员时跳过、解散事件不查成员。
- 宿主机模式 e2e 32 项通过，新增 `TestKafkaRetryTopicAndDeadLetter`（经真实 Kafka：主 topic 尝试 2 次后转入 `marschat.e2e.retry`，1 秒后处理成功；死信记录带原事件 ID、消费组和原 topic）与 `TestDeliverySocialNotifications`（好友申请、成为好友、建群、入群申请只推给群主、审核入群推给全部成员和新成员）。
- `TestSocialCacheInvalidatedByEvent` 首次运行失败：delivery 也消费它注入的 `GroupUpdated`，经 `ListAllGroupMembers` 查成员时 social 先读群资料缓存，删除后被重新填充为空值标记。缓存行为正确，断言从“键不存在”改为“不再是旧值”，并记录被重新填充的键。
- `buf lint`、`go vet ./...`、`go build ./...`、`go test -race ./...`（27 个包）通过；重建镜像后 `make cluster-test` 6 项通过，`marschat-delivery` 分到 `marschat.social.events` 的 6 个分区，`marschat-social-cache-retry` 正常启动，各容器日志无 ERROR。重试队列在真实故障下的表现放到第 5 段的 Redis 故障实验。

第 5 段验证（2026-11-20）：

- `pkg/bootstrap/redis_test.go`：
  - 模拟每次拨号要等 100 毫秒才失败的 Redis：第一次拨号真的拨号，之后的拨号在 50 毫秒内失败，恢复后拨号恢复；
  - 调用方取消不触发标记；
  - miniredis 关闭再重启后，客户端在探测间隔内恢复；
  - 去掉快速失败分支后用例失败。
- 故障实验 7 项与集群测试 6 项通过（`make cluster-test`），各项数据记在[重构计划的故障实验结果](../plans/active/backend-refactor.md#故障实验结果)。首次运行时两项 Redis 实验失败，见上表第 5 段第一行；修复后通过。
- 每次 Redis 故障，各服务只在开始和恢复时各记一条日志（`redis unreachable, failing dials fast until a probe succeeds`、`redis reachable again`）。60 秒实验中，message 的 ERROR 只有发号失败的访问日志（TD-35）。go-redis 内部的纯文本拨号失败日志仍然逐次输出（TD-36）。两项均已于 2026-11-20 解决：发号失败返回 503、访问日志记 WARN；go-redis 内部日志转到 `slog`，拨号失败记 DEBUG。
