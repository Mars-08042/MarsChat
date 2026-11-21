# 计划：后端重构

- 状态：已完成 | 开始：2026-11-18 | 完成：2026-11-21 | 分支：`dev` | 关联设计：[目标架构图](../../images/proposed-distributed-architecture.svg)、[ADR-0005](../../architecture/adr/0005-distributed-architecture.md)、[ADR-0006](../../architecture/adr/0006-message-sync-model.md)

## 目标

在 `dev` 分支上用 Kratos v3 重写后端：按目标架构拆分为 access、account、social、message、delivery、media 六个服务，接口全部用 protobuf 定义。实现可靠消息（幂等、会话内有序、事务 Outbox、断线补拉）、多端同步、跨节点投递和慢连接隔离，并用端到端测试、故障实验和压测验证。

项目用于求职展示，适合本项目的生产环境技术会引入，每项都要对应一个实际要解决的问题（见“生产环境技术点”）。

前端重构另立计划（React + pnpm），本计划只负责移除旧的 Vue 前端。

## 结果

2026-11-21 完成。六个服务、通话信令、网关与多实例、可观测与压测全部实现并验证，当前架构见 [ARCHITECTURE.md](../../../ARCHITECTURE.md)，功能状态见 [features.md](../../product/features.md)。前端重构另立计划。

| 步骤 | 提交 |
|---|---|
| 0. 准备与清理 | `6624cea`（计划与目标架构图）、`041559f`（移除旧代码） |
| 1. 基础设施与工程骨架 | `6685aae` |
| 2. account 服务 | `bfe2778` |
| 3. social 服务 | `edf0492` |
| 4. 核心消息链路 | `7f36418` |
| 5. 网关与多实例 | `1f8e77e`、`52eb184`、`7d20975` |
| 插入任务：邮箱验证码 | `0749b90` |
| 6. media 服务 | `f1ade90`；OSS 手动验证与修复 `452b2a9` |
| 7. 通话信令 | `66ea720` |
| 8. 可观测与压测 | `21389c8` |
| 9. 收尾 | 本计划移到 `completed/` 的提交 |

最终验证（2026-11-21，收尾之后）：`buf lint`、`make gen` 后无差异、`go vet`（含 `e2e`、`cluster` 标签）、`go build`、`go mod tidy -diff`、`go test -race ./...`（31 个包）、`make rules-test` 通过；文档中的相对链接全部可达。宿主机模式 `make e2e` 与容器模式 `make cluster-test`（17 项，含 7 项故障实验）最后一次全量通过是步骤 8（收尾只改了文档和 Makefile 的 `dlq` 目标）。可靠性与性能数据见[故障实验结果](#故障实验结果)和[压测结果](#压测结果)。

遗留：

- **随前端重构**：coturn 部署、ICE 服务器与 TURN 临时凭据下发、群通话（步骤 7）；WebRTC 媒体在浏览器中的连通验证；通话记录。后端尚未实现的功能见 [features.md](../../product/features.md#未实现)。
- **步骤 10 的可选扩展**未实施：MySQL 主从与读写分离、Consul KV 配置中心、testcontainers 数据层集成测试。
- **未关闭的技术债**（见 [tech-debt.md](../tech-debt.md)）：中优先级 TD-42（delivery 全部停止后没有告警）、TD-26（服务间 gRPC 没有认证）；低优先级 TD-25、TD-28、TD-32、TD-33、TD-37、TD-38、TD-43。

## 范围与约定

- 删除旧后端代码、旧 API、旧表定义和旧前端；数据库表按新项目重新设计，不迁移旧数据。
- 只删除 MarsChat 自己的容器、卷和镜像。`redis:7` 镜像同时被其他项目的 `pestle-redis` 使用，保留。
- 版本：Kratos v3.0.0（当前稳定版，要求 Go 1.25+，本机 Go 1.27.3）；镜像在步骤 1 选用当时最新的稳定版并锁定具体标签，不使用 `latest`。
- 容器宿主机端口为容器默认端口 + 10000（见下表），只绑定 `127.0.0.1`。
- 仓库组织：六个后端服务都放在本仓库，用单个 Go module，module 路径改为 `github.com/Mars-08042/MarsChat`。服务是否独立看进程和数据归属，不看仓库。只有两种情况才拆出独立仓库：需要被其他项目复用的公共库，以及技术栈完全不同的部分（例如新前端，在前端计划中决定）。拆分时在 GitHub 建私有仓库，在 `D:\Desktop\Projects` 下建本地仓库，Go 代码通过 GitHub 模块路径引用（配置 `GOPRIVATE`，本地联调用 `go.work`）；创建 GitHub 仓库属于对外操作，每次先征得开发者同意。
- 开发阶段服务在宿主机运行，依赖组件放在 Compose；从步骤 5 起服务也打包进容器，用于多实例实验。
- 旧的横切约定（三元返回值、`JsonBack`、`zlog`）随旧代码废弃；新约定（Kratos errors、`slog`、protobuf 契约）写入 ADR-0005 和 `system.md`。
- 各服务的接口字段、表结构、缓存键、配置项在实施对应步骤时确定，写入模块文档和 `data-model.md`，本计划只列原则。
- 本计划确认后，其中列出的依赖变更和 `go.mod` 重置视为已同意；计划外的新依赖仍先询问。

## 端口

| 组件 | 容器端口 | 宿主机端口 | 引入步骤 |
|---|---|---|---|
| MySQL | 3306 | 13306 | 1 |
| Redis | 6379 | 16379 | 1 |
| Kafka（外部监听） | 9092 | 19092 | 1 |
| Consul（HTTP / UI） | 8500 | 18500 | 1 |
| APISIX（HTTP / HTTPS） | 9080 / 9443 | 19080 / 19443 | 5 |
| Prometheus | 9090 | 19090 | 8 |
| Alertmanager | 9093 | 19093 | 8 |
| Grafana | 3000 | 13000 | 8 |
| Jaeger（UI / OTLP gRPC / OTLP HTTP） | 16686 / 4317 / 4318 | 26686 / 14317 / 14318 | 8 |
| k6（压测，只在压测时运行，固定 IP `172.30.0.20`） | — | — | 8 |

应用服务按 HTTP `18xx0`、gRPC `19xx0` 编排（2026-11-18 确认）：access 18000（WebSocket）/ 19100（内部 gRPC，2026-11-19 确认；19000 原本留给 MinIO，2026-11-20 改用本地目录与阿里云 OSS 后不再部署 MinIO，见 ADR-0007）、account 18010 / 19010、social 18020 / 19020、message 18030 / 19030、delivery 18040 / 19040（HTTP 只有探针与 `/metrics`，步骤 8 新增）、media 18050 / 19050。

2026-11-18 检查：上述端口中只有 19093 被占用，占用者是旧的 `marschat-kafka-1`，步骤 0 删除后释放。

## 数据库与缓存设计原则

表结构在各步骤实施时设计，统一遵守以下原则。

**数据库**

- 每个服务独立建库，不跨库联表、不使用外键，跨服务数据通过接口或事件获取。
- 表结构用版本化 SQL 迁移管理，不用 AutoMigrate；字符集 `utf8mb4`。
- 事务只包住数据库操作，不在事务内调用外部服务；需要串行化的计数（如会话 seq）用行锁在事务内分配。
- 每个查询都有对应索引，联合索引按查询条件排列，关键查询用 `EXPLAIN` 验证；列表一律游标分页，不用深 `OFFSET`。
- 幂等和去重依赖唯一约束；处理好软删除与唯一索引的冲突；并发修改用乐观锁（版本号）。
- 主键、索引以会话 ID 等分片键开头，保证将来能按会话分片，但当前不分表。

**缓存**

- 采用 Cache-Aside：先写库再删缓存，删除失败经消息队列重试。只缓存读多写少的数据（用户资料、群资料、群成员、会话摘要），消息正文以数据库为准。
- 缓存穿透：参数校验、空值缓存（短 TTL）、用户与群 ID 的布隆过滤器。
- 缓存击穿：用 singleflight 合并进程内的并发回源，再用 Redis 互斥锁限制跨实例重建；热点数据可用逻辑过期。
- 缓存雪崩：TTL 加随机抖动；热点数据加本地缓存；Redis 不可用时降级读库，同时限流、熔断。

## 生产环境技术点

| 技术点 | 解决的问题 | 步骤 |
|---|---|---|
| 消息队列 Kafka（按会话分区、消费组） | 消息异步分发、削峰、多实例扩容 | 4、5 |
| 事务 Outbox（本地消息表） | 写库与发消息一致，跨服务最终一致 | 4、5 |
| 重试队列与死信队列 | 消费失败可重试、可排查，不阻塞分区 | 5 |
| 消费幂等 | 至少一次投递下不重复处理 | 4、5 |
| Outbox 多实例发布（Redis 租约，每个服务只有一个发布者；步骤 4 起替代原计划的 `FOR UPDATE SKIP LOCKED`） | 多实例下事件按顺序发布；持有者故障时其他实例在租约 TTL 内接手，重复由消费幂等吸收 | 4、5 |
| Redis 在线路由 + 定向推送 | WebSocket 多实例下跨节点投递 | 4、5 |
| 缓存三类问题防护（见上节） | 穿透、击穿、雪崩 | 2、3 |
| 限流（网关入口 + Redis 按用户滑动窗口） | 防刷消息、防短信轰炸 | 2、4、5 |
| 短信防刷（发送间隔、每日上限、按 IP 限流、验证次数上限、一次性使用） | 短信成本与暴力破解 | 2 |
| JWT + 刷新令牌轮换 + Redis 吊销名单 | 无状态鉴权，同时支持退出、封禁即时失效 | 2 |
| snowflake 节点号用 Redis 租约分配，租约丢失后换用新节点号 | 多实例生成 ID 不冲突；Redis 长时间故障恢复后自动恢复发号 | 2、5 |
| protovalidate 参数校验 | 在接口契约中声明校验规则 | 2 |
| 超时、重试（仅幂等调用）、熔断 | 依赖故障时不雪崩 | 5 |
| 优雅停机（access 通知客户端重连、先从 Consul 摘除） | 滚动发布不丢消息 | 5 |
| 链路追踪、指标、告警、慢查询日志 | 定位延迟与故障，验证索引 | 8 |
| 压测与故障实验 | 用数据证明扩展性与可靠性 | 5、8 |
| GitHub Actions（lint、vet、单测、构建） | 每次提交自动检查 | 1 |

**不引入**（面试时可说明理由）：Elasticsearch（消息搜索不在范围内，资源占用大）；Kubernetes 和服务网格（Compose 已能演示多实例与故障实验，可作为后续扩展）；分库分表中间件（数据量用不到，表设计已预留分片键）；Seata 和两阶段提交（用 Outbox 实现最终一致）。

## 步骤

每一步完成时同步对应文档（见 AGENTS.md 文档同步表），并运行该步列出的验证。

### 0. 准备与清理

- [x] 创建本地 `dev` 分支（2026-11-18，未推送）；计划与架构图提交为 `6624cea`。
- [x] 编写 ADR-0005（目标架构与技术选型）和 ADR-0006（消息同步模型：读扩散、会话内 seq、游标补拉），状态为提议中；ADR-0001、0002 标为已被 ADR-0005 替代，ADR-0003 标为已被 ADR-0006 替代，ADR-0004 注明旧前端已移除、pnpm 约定沿用到新前端。
- [x] 开发者确认 ADR-0005、ADR-0006，状态改为已采纳（2026-11-18）。
- [x] 删除旧代码：`cmd/mars_chat_server/`、`api/v1/`、`internal/`、`pkg/`、`test/`、`configs/`、`static/`、`mars_chat_server.exe`、`web/chat-server/`，以及对应旧容器的 `compose.yaml`；`go.mod` 按新 module 路径重新初始化，`go.sum` 删除。`.env.example` 与被忽略的 `.env` 暂留，步骤 1 随新 compose 更新。
- [x] 删除旧容器与数据：容器 `marschat-mysql-1`、`marschat-redis-1`、`marschat-kafka-1`、`marschat-kafka-init-1`，网络 `marschat_default`；卷 `marschat_mysql-data`、`marschat_redis-data`、`marschat_kafka-data`；镜像 `mysql:8.0`、`apache/kafka:4.3.1`。复核：无 MarsChat 残留，`redis:7` 镜像保留，`pestle-redis` 正常运行。
- [x] 文档：`tech-debt.md` 清空旧基线条目，新问题从 TD-24 起编号；`features.md` 改为旧基线功能基线（列名“旧基线状态”）；`internal/service/chat/AGENTS.md` 随 `internal/` 删除；重写 `AGENTS.md`；`README.md`、`ARCHITECTURE.md`、`docs/README.md`、`system.md`、`data-model.md`、`data-flow.md`、`deployment.md`、`overview.md` 和 6 篇模块文档加“重构说明”。

### 1. 基础设施与工程骨架

- [x] 新 `compose.yaml`：MySQL 9.7.4、Redis 8.8.4、Kafka 4.4.0（KRaft）、Consul 2.1.0，含健康检查；MySQL 初始化脚本为每个服务建库建账号。
- [x] 单 Go module 的 monorepo：`app/account`、`app/social` 按 Kratos v3 分层（`cmd`、`internal/{conf,data,server}`、`configs`），公共启动组件在 `pkg/bootstrap`，共用配置消息在 `pkg/conf`。
- [x] buf 管理配置 proto：`buf.yaml`（`pkg`、`app` 两个模块）、`buf.gen.conf.yaml`（`protoc-gen-go` 用 `go run` 固定版本）；`make gen` 在 Git Bash 下可运行，两次生成结果一致。
- [x] 公共能力：配置加载与 `MARSCHAT_` 环境变量覆盖、`slog` 日志（GORM 日志接入）、Consul 注册发现（含 `pkg/balancer` 对 Kratos 负载均衡器的修复）、GORM 连接池、Redis 客户端、存活与就绪探针；wire 依赖注入；Makefile。
- [x] 确认 Kratos v3 接入方式：Consul 注册用 `contrib/registry/consul/v3`（默认 TCP 检查加 TTL 心跳）；gRPC 服务默认注册健康服务；JWT、OpenTelemetry、参数校验在 contrib 模块中，具体接入随使用它们的步骤确定。
- [x] GitHub Actions：buf lint、生成代码一致性、`go mod tidy -diff`、go vet、构建、`go test -race`（推送后才会运行，推送需开发者同意）。
- [x] 验证（2026-11-18）：
  - `buf lint`、`go vet ./...`、`go build ./...`、`go test ./...`、`go mod tidy -diff` 通过；`make gen` 两次结果一致。
  - 四个容器健康；四个服务账号只能访问本库（访问其他库被拒绝），字符集 `utf8mb4` / `utf8mb4_0900_ai_ci`；Redis 无密码访问被拒绝；Consul 有 leader；四个宿主机端口可连接。
  - account、social 启动后注册到 Consul（gRPC 与 HTTP 两个端点，TTL 检查 passing），`/healthz`、`/readyz` 返回 200，`make e2e` 通过。
  - 强制结束服务进程后，TTL 到期实例变为 critical，服务发现返回 0 个实例。
  - 未验证：Kafka 外部监听器的收发（宿主机暂无 Kafka 客户端，步骤 4 验证）；收到 Ctrl+C 时从 Consul 主动注销（后台任务只能强制结束进程，需要在终端中手动验证）；CI（未推送）。

实施中的调整（2026-11-18）：以下内容在步骤 1 还没有使用方，按“简单优先”移到第一次用到它们的步骤，避免先写出没有调用者的代码。

| 内容 | 移到 | 原因 |
|---|---|---|
| 接口 proto 的 gRPC、HTTP、OpenAPI 生成，buf breaking 检查，protovalidate | 2 | 步骤 1 还没有接口 proto |
| 错误码 | 2 | 随第一个业务接口确定；用 Kratos errors 手写构造，不用 errors 代码生成器 |
| snowflake ID、SQL 迁移（`migrate/migrate` 镜像）、带三类防护的缓存组件、Redis 限流组件 | 2 | account 是第一个使用方 |
| Kafka topic 初始化 | 4 | 第一个 topic 随 Outbox 发布器出现 |
| OpenTelemetry 中间件 | 8 | 与 Jaeger 一起接入 |

另外，“两个空服务互相调用”改为由端到端测试通过服务发现调用两个服务的 gRPC 健康检查，服务之间真正的调用从步骤 3 开始。本地开发时 Consul 只用 TTL 心跳，不做 TCP 探测（服务监听宿主机 `127.0.0.1`，容器里的 Consul 探测不到），步骤 5 服务容器化后开启。

### 2. account 服务

设计：[account-service.md](../../design/account-service.md)。

- [x] 从步骤 1 移入的公共能力：接口 proto 生成（gRPC、HTTP、OpenAPI）、buf breaking 检查（CI）、protovalidate、错误码、snowflake ID（节点号用 Redis 租约分配）、按服务分库的 SQL 迁移（`migrate/migrate` 镜像）、带三类防护的缓存组件、Redis 限流组件。另外新增 `pkg/jsoncodec`（HTTP 按 protobuf JSON 收发）和 `pkg/middleware/accesslog`（不记录请求体的访问日志）。
- [x] 手机号加短信验证码注册；密码登录与短信验证码登录（2026-11-20 改为邮箱，见下方“插入任务：邮箱验证码”）；刷新令牌、退出、个人资料、设备列表与踢设备、管理员封禁与解封。
- [x] 短信（2026-11-20 改为邮箱验证码，SMTP 发送方已实现）：开发环境只写日志的发送方已实现；验证码存 Redis，加上发送间隔、每日上限、按 IP 限流、验证次数上限，验证后立即失效，限额可热更新。**阿里云短信未实现**（没有 AccessKey，TD-24）。
- [x] 密码用 bcrypt 哈希；JWT（Ed25519）访问令牌加按设备签发的刷新令牌（使用后轮换、重放检测）；退出、踢设备和封禁时写入 Redis 吊销名单；公共 JWT 中间件在 `pkg/auth`。
- [x] 用户资料缓存接入缓存组件，加布隆过滤器；封禁和踢设备时写 Outbox 事件（发布器在步骤 4 实现）。
- [x] 验证：单元测试；HTTP 接口冒烟测试；e2e 覆盖完整流程与短信限流。

### 3. social 服务

设计：[social-service.md](../../design/social-service.md)。

- [x] 好友：申请、同意、拒绝、删除、拉黑。群组：创建、直接加入或审核加入、邀请好友、成员与角色、转让、退出、解散、禁言。
- [x] 对内 gRPC：发言权限校验（给 message 用）、群成员列表（给 delivery 用，接入缓存组件并开启跨实例重建锁）、批量群资料。
- [x] 群资料修改用乐观锁；好友与群的变化写 Outbox 事件。由 message 创建或更新会话属于步骤 4（发布器与消费方）。
- [x] 以旧模块文档 [contact.md](../../product/modules/contact.md)、[group.md](../../product/modules/group.md) 为功能基线，同时修正 TD-01、TD-16、TD-17 中的逻辑错误。
- [x] 公共组件：`pkg/bloom`（从 account 抽出，补上 TD-27 的失败分支测试）、`pkg/cache` 重建锁、`bootstrap.ServerMiddlewares`；social 经 Consul 调用 account。
- [x] 验证：单元测试；e2e 经 HTTP 和 gRPC 调用主要接口（代替手工冒烟测试）；关键查询 `EXPLAIN`。

### 4. 核心消息链路（各服务单实例）

设计：[message-pipeline.md](../../design/message-pipeline.md)（已采纳，实施差异见其第 9 节）。

- [x] access：WebSocket 接入、首帧校验令牌、心跳、每连接有界发送队列（满了断开慢连接）、帧协议用 proto 定义、Redis 在线路由（用户 → 连接 → 实例，字段级 TTL 随心跳续期）、每连接帧频率保护、收到封禁与吊销事件断开连接。按用户限制发送频率改在 message 用 Redis 统一计数（设计第 8 节第 9 点）。
- [x] message：会话模型（读扩散）、按 `client_msg_id` 幂等（唯一约束）、事务内用行锁分配会话 seq、消息与 Outbox 同事务、历史游标分页、会话列表（最后一条消息、未读数）、已读位置、按游标补拉（完整、分批、缺口过大重载三种结果）；群会话成员由 social 事件维护。
- [x] Outbox 发布器与 Kafka（按会话分区，topic 由初始化任务显式创建；发布器用 Redis 租约，account、social、message 都启用）；delivery：消费事件（幂等）、查在线路由、推送给在线设备并排除发起连接、群成员扩散（原属步骤 5）、踢连接；social 经事件兜底删除群缓存（TD-25 的 social 部分）。
- [x] 在 `test/e2e` 编写端到端测试（Go WebSocket 客户端），覆盖：发送确认、重复发送只存一条、断线补齐（`SLICE`；`TOO_LONG` 只有单元测试）、多端同步、已读与未读、群推送、封禁踢线。

### 5. 网关与多实例

- [x] APISIX Standalone：HTTP 按路径直达各服务（通过 Consul 发现），WebSocket 转发到 access，TLS 与入口限流。先验证 Standalone 与 Consul 发现能组合使用（官方文档未见组合示例）。（2026-11-19，组合实验通过）
- [x] 服务镜像与 Compose 多实例（access、message、delivery 各起 N 个）；验证 Outbox 租约在多实例下的接管（步骤 4 已用 Redis 租约替代 `FOR UPDATE SKIP LOCKED`，见 message-pipeline 设计第 8 节第 5 点，TD-32）。（2026-11-20，各起 2 个；租约接手见[故障实验结果](#故障实验结果)）
- [x] delivery 与消费者完善（直连、缓存连接、群成员扩散、尽力推送已在步骤 4 实现）：消费失败进重试队列，多次失败进死信队列（TD-30）；access 实例下线时摘除缓存的连接（TD-31）；social 事件的实时通知（好友申请、入群申请等）。
- [x] 服务间调用加超时、仅对幂等调用重试、熔断；access 优雅停机（通知客户端重连、先从 Consul 摘除）。
- [x] 故障实验：跨节点收发；杀掉 access 或 delivery 实例；滚动重启 access；暂停 Kafka；停掉 Redis 验证降级。验证不丢、不重、重连能补齐，结果记录在本计划。（2026-11-20，见[故障实验结果](#故障实验结果)）

### 6. media 服务

- [x] ~~MinIO（S3 兼容）存储~~ 可切换的存储（默认仓库下的本地目录，可选阿里云 OSS）；上传时由服务端生成文件 ID 并限制大小和类型；下载前校验权限；文件消息引用文件 ID；图片生成缩略图。
  - 2026-11-20 核实：MinIO 社区版已归档，Docker Hub 镜像已删除。开发者决定存储可切换，默认本地目录 `data/media/`，实现阿里云 OSS 集成，聊天文件上限 100 MB，见 [ADR-0007](../../architecture/adr/0007-object-storage.md) 与设计 [media-service.md](../../design/media-service.md)（已采纳）。
  - [x] 1. media 骨架：proto、配置、迁移、wire、Consul 注册、镜像与 Compose、Makefile；验证容器内挂载目录的写权限（2026-11-21：迁移 up → down 1 → up 往返通过；Docker Desktop 下 nonroot 用户可写挂载目录）
  - [x] 2. 存储接口与 local、oss 两种实现，单元测试（2026-11-21：OSS 预签名离线校验、上传与删除对接假服务器）
  - [x] 3. 上传、缩略图、下载地址、本地下载、内部 `GetFiles`、元数据缓存，单元测试与 e2e（2026-11-21：`EXPLAIN` 记在 data-model.md）
  - [x] 4. message 接入图片与文件消息，e2e（2026-11-21）
  - [x] 5. 网关路由、集群测试、文档（2026-11-21：宿主机模式 `make e2e` 通过；`make cluster-test` 15 项中 14 项首次通过，图片消息用例因测试未注册 JPEG 解码器失败，补上后单独重跑通过；内存实测后调整缩略图并发与内存预算，见设计第 10 节；调整后全量检查、e2e、集群测试 15 项通过，验收中发现并修复 TD-39）
  - [x] 6. OSS 手动验证（需要开发者的 Bucket 与 AccessKey）。2026-11-21：容器模式经网关向开发者的 Bucket 上传图片与普通文件，对象名按年、月分目录；预签名地址下载的内容一致，Range 返回 206，篡改签名返回 403。验证中发现并修复预签名地址覆盖 `Content-Type` 被 OSS 拒绝（400）的问题，见设计第 10 节。Chrome 中 `<img>` 正常显示（默认域名强制 `attachment` 不影响嵌入）；上传 20 MiB 约 3.8 秒、从 OSS 下载约 3.0 秒（开发机公网带宽）

### 7. 通话信令

设计：[call-signaling.md](../../design/call-signaling.md)（已采纳，实施中的调整见其第 9 节）。

- [x] 信令帧（邀请、接听、拒绝、挂断、SDP、ICE）走 WebSocket；access 按在线路由直转对端实例，不落库、不经 Kafka；对方离线时立即返回失败；多端来电，一端接听后其他端提示“已在其他设备接听”。（2026-11-21：另加只能呼叫好友、一人一通的忙线判断、服务端振铃超时、断线结束；delivery 的 access 直连客户端抽到 `pkg/accessclient` 共用）
- [ ] coturn 推迟到前端重构时部署：TURN 中继端口段难以套用 +10000 规则，届时再单独确认。群通话、ICE 服务器与 TURN 临时凭据下发一并推迟（设计第 8 节问题 7）。

### 8. 可观测与压测

设计：[observability.md](../../design/observability.md)（已采纳，实施中的调整见其第 10 节）。

- [x] Prometheus、Grafana、Alertmanager；指标包括连接数、消息端到端延迟、推送失败数、Kafka 积压、缓存命中率。（2026-11-21：Prometheus 经 Consul 发现全部实例；8 条告警规则带 promtool 单元测试；`InstanceDown`、`KafkaConsumerLag` 实际触发并恢复各一次）
- [x] OpenTelemetry + Jaeger，消息 ID 贯穿日志与 Trace；开启 MySQL 慢查询日志。（2026-11-21：消息没有单独的 ID，用 `(conversation_id, seq)` 和发送方的 `client_msg_id` 作为 Span 属性；Trace 经 Outbox 列 `trace_parent` 和 Kafka 记录头跨越异步段，一条消息从 access 到 delivery 再到 access 是一条 Trace）
- [x] 用 k6（Docker 镜像运行）压测 WebSocket 收发和热点读接口，记录在线连接数、P99 延迟和缓存防护效果。（2026-11-21：结果见[压测结果](#压测结果)）

### 9. 收尾

- [x] 复核 `ARCHITECTURE.md`、`system.md`、`data-model.md`、`data-flow.md`、`deployment.md`、`features.md` 和模块文档；本计划补上结果后移到 `plans/completed/`。
  - [x] 1. 遗留验证：OSS 手动验证剩余的浏览器 `<img>` 显示与耗时记录（2026-11-21，容器模式临时切到 oss，验证后已按 `.env` 恢复为 local）
  - [x] 2. 总览文档：重写 `README.md`、`ARCHITECTURE.md`、`overview.md`，更新 `docs/README.md`；目标架构图改为当前架构并与代码核对
  - [x] 3. 产品文档：按开发者要求（2026-11-21）直接删除旧基线描述，避免歧义：`features.md` 的旧基线功能表、6 篇模块文档的旧章节、4 张旧基线插图和 2 篇只针对旧代码的经验文档；设计文档与 ADR 中作为背景的旧基线说明保留
  - [x] 4. 架构与部署文档：`system.md`、`data-model.md`（删除旧基线附录）、`data-flow.md`、`deployment.md` 与代码核对（文档中的 `make` 命令、宿主机端口）；发现告警说明与可观测设计引用的 `make dlq` 不存在，补上该目标
  - [x] 5. 计划收尾：补上结果与遗留项（推迟到前端的 coturn 等、步骤 10 的可选扩展、未关闭的技术债），移到 `plans/completed/` 并更新引用
  - [x] 6. 验证：`buf lint`、`make gen` 后无差异、`go vet`、`go build`、`go test -race ./...`，文档内相对链接全部可达

### 10. 可选扩展（未实施）

主线完成后没有选择实施，留作后续候选。

- [ ] MySQL 主从复制与读写分离（GORM dbresolver），处理复制延迟下的“写后读”。
- [ ] Consul KV 作为配置中心，支持动态配置。
- [ ] 用 testcontainers 做数据层集成测试。

## 可参考 teamgram 的部分

路径相对 `tmp/teamgram-server/`。teamgram 基于 go-zero + etcd，我们参考的是做法，不照搬代码。

| 步骤 | 参考内容 | 位置 | 与本项目的差异 |
|---|---|---|---|
| 1 | 多服务目录划分、每服务独立配置、基础设施 Compose | `app/`、`teamgramd/etc/`、`docker-compose-env.yaml` | 我们用 Kratos 分层和 Consul |
| 1 | snowflake ID | `app/service/idgen/internal/dao/dao.go` | 进程内生成，不建独立的 idgen 服务 |
| 2 | 登录会话与设备管理 | `app/service/authsession/`、`app/bff/authorization/` | 我们用 JWT + 刷新令牌 |
| 3 | 联系人与普通群逻辑 | `app/bff/contacts/`、`app/bff/chats/`、`app/service/biz/chat/` | — |
| 4 | 防重复发送：Redis 锁加结果缓存，重复请求返回首次结果 | `app/messenger/msg/internal/dao/dao.go` | 我们以数据库唯一约束为准 |
| 4 | 发送方同步写、接收方异步写 | `app/messenger/msg/internal/dao/outbox.go`、`inbox.go` | 我们用读扩散，不为每个成员存副本 |
| 4 | 更新记录与补拉的三种结果 | `app/messenger/msg/internal/dao/pts.go`、`app/bff/updates/internal/core/updates.getDifference_handler.go` | 我们按会话 seq 补拉，不用每用户 pts |
| 4 | 有界发送队列 | `app/interface/session/internal/sess/session_outgoing_queue.go` | — |
| 4 | 在线状态登记与 TTL 续期 | `app/service/status/internal/core/status.setSessionOnline_handler.go`、`app/interface/session/internal/sess/main_auth_wrapper.go` | — |
| 4、5 | Kafka 生产者分区键 | `app/messenger/msg/inbox/client/inbox_mq_client.go` | 我们按会话分区，并且先经过 Outbox |
| 5 | 查路由、按实例分组推送、排除发起设备、推送尽力而为 | `app/messenger/sync/internal/core/core.go` | — |
| 5 | 按节点缓存客户端、监听注册中心变化、连续失败摘除 | `app/interface/gnetway/internal/dao/sharding_session_client.go` | 我们按路由定向推送，不做一致性哈希 |
| 6 | 文件服务与 MinIO 初始化 | `app/service/dfs/`、`app/service/media/`、`minio_init.sh` | 我们不用 MinIO（ADR-0007）；只参考上传时同步生成缩略图、随机访问密钥 |
| 8 | Prometheus、日志收集、链路追踪 | `teamgramd/deploy/prometheus/`、`docs/log-collection-zh.md`、`docs/link-tracking-zh.md` | 我们用 OpenTelemetry + Jaeger |

不参考：MTProto 协议层、写扩散与按用户分表、独立的 idgen 服务、12 个服务的拆分粒度。teamgram 写库与投递 Kafka 不在同一事务中，pts 记录写失败也只打日志，我们用事务 Outbox 避免这两个问题。

## 已确认的决定（2026-11-18）

- 保留手机号加短信验证码注册，以及短信验证码登录。（2026-11-20 已改为邮箱验证码，见[插入任务：邮箱验证码](#进度记录)）
- 应用服务端口按 HTTP `18xx0`、gRPC `19xx0` 编排。
- 数据库表按新项目重新设计，考虑事务、索引和缓存三类问题。
- 适合本项目的生产环境技术可以引入。

## 进度记录

步骤 0 已完成（2026-11-18）。旧的本地配置（含阿里云短信 AccessKey）未保留，步骤 2 接入短信时重新配置。

步骤 1 已完成（2026-11-18），提交 `6685aae`。

步骤 2 已完成（2026-11-18），提交 `bfe2778`，阿里云短信除外（TD-24；2026-11-20 改为邮箱验证码后不再适用）。设计 [account-service.md](../../design/account-service.md) 已采纳，实施中的调整记在其第 8 节。验证：`buf lint`、`go vet`、`go build`、`go test -race ./...`、`make e2e`（6 个用例）通过，关键查询 `EXPLAIN` 已核对。

步骤 3 已完成（2026-11-19），提交 `edf0492`。设计 [social-service.md](../../design/social-service.md) 已采纳（第 8 节的 7 个问题均按推荐方案确认），实施中的调整记在其第 9 节。验证：`buf lint`、`go vet`（含 `e2e` 标签）、`go build`、`go test -race ./...`、`make gen` 后无差异、`make e2e`（17 个用例，连续两次）通过，`EXPLAIN` 已在造数据的临时库中核对。

步骤 4 已完成（2026-11-19），提交 `7f36418`。设计 [message-pipeline.md](../../design/message-pipeline.md) 已采纳（第 8 节的 9 个问题均按推荐方案确认），实施中的调整记在其第 9 节。按第 7 节的顺序分五段实施：

- [x] 1. Kafka topic 初始化、`pkg/kafka`、Outbox 发布器（account、social 启用）、Kafka 集成测试（2026-11-19：单测、`make e2e` 19 项通过；首次启动时发现 franz-go 按记录时间戳计算投递超时，见 [lessons/franz-go-record-timestamp-timeout.md](../../lessons/franz-go-record-timestamp-timeout.md)）
- [x] 2. message（2026-11-19：biz 单测、message e2e 3 项通过；并发发送无死锁）
- [x] 3. access（2026-11-19：单测含 `-race`、access e2e 4 项通过；`HSETEX` 需要 Redis 8.0+，设计写的 7.4 有误）
- [x] 4. delivery 与 social 缓存消费者（2026-11-19：delivery 单测 9 项；推送、多端同步、已读回执、群推送、封禁踢线、缓存兜底删除的 e2e 通过）
- [x] 5. e2e、`EXPLAIN`、文档（2026-11-19：`make e2e` 30 项连续两次通过；`EXPLAIN` 记在 data-model.md；data-flow.md 重写，system、deployment、features、chat-message、tech-debt 同步更新）

验证：`buf lint`、`make gen` 后生成代码不变、`go mod tidy -diff`、`go vet`（含 `e2e` 标签）、`go build`、`go test -race ./...`（24 个包）、`make e2e`（五个服务同时运行，30 项，连续两次）通过，各服务日志无 ERROR。

下一步：步骤 5（网关与多实例）。开始前先写设计文档，重点是 APISIX Standalone 与 Consul 发现的组合验证、多实例下的跨节点推送与故障实验，以及 TD-30（重试与死信队列）、TD-31、TD-32。

步骤 5 实施中。设计 [gateway-multi-instance.md](../../design/gateway-multi-instance.md) 已采纳（2026-11-19，第 8 节的 5 个问题均按推荐方案确认），按其第 7 节分五段实施：

- [x] 1. 容器模式：端点顺序、Consul 检查间隔、access 路由地址自动获取、消费组会话超时、镜像、Compose `apps` profile、固定子网、Kafka 堆内存（2026-11-19：8 个应用容器注册正常，宿主机模式 `make e2e` 通过；MarsChat 内存合计约 1.2 GiB；`health_check` 改为 `tcp_check_interval`，见设计第 9 节）
- [x] 2. 网关：Standalone 与 Consul 组合实验，路由、WebSocket、限流、可信代理、TLS（2026-11-19：组合实验通过；`make cluster-test` 5 项通过，含跨 access 实例推送；发现跨实例 Outbox 唤醒缺失，第 3 段修复）
- [x] 3. 调用韧性：重试、熔断、TD-32、TD-31、TD-34、Outbox 跨实例唤醒、access 优雅停机（2026-11-19：`go test -race ./...` 27 个包通过；`make cluster-test` 6 项通过，跨实例推送 8～14 ms；宿主机模式 `make e2e` 通过，日志无 ERROR。TD-31、TD-34 已解决；TD-32 核对后发现检查在步骤 4 已实现，残留风险改写后保留在“低”，见设计第 9 节）
- [x] 4. 消费韧性与通知：死信队列与重试队列（TD-30）、social 实时通知（2026-11-20：`go test -race ./...` 27 个包通过；宿主机模式 e2e 32 项通过，新增重试队列与死信队列往返、好友与群通知；`make cluster-test` 6 项通过。TD-30 已解决；没有新增 `Ordered` 字段，见设计第 9 节）
- [x] 5. 故障实验与文档（2026-11-20：`make cluster-test` 13 项通过，含 7 项故障实验，结果见[故障实验结果](#故障实验结果)；Redis 故障实验发现停机后拨号把每条命令拖到几秒，`pkg/bootstrap` 加拨号保护后通过，见 [lessons/redis-dial-stall-docker-dns.md](../../lessons/redis-dial-stall-docker-dns.md)；新增 TD-35、TD-36）

步骤 5 已完成（2026-11-20），提交 `1f8e77e`（第 1～3 段）、`52eb184`（第 4 段）、`7d20975`（第 5 段）。实施中的调整记在设计第 9 节。验证：`buf lint`、`go vet`（含 `e2e`、`cluster` 标签）、`go build`、`go test -race ./...`、宿主机模式 `make e2e`、容器模式 `make cluster-test` 通过。

插入任务：邮箱验证码（2026-11-20，提交 `0749b90`）。个人认证的阿里云账号无法申请短信签名（TD-24），开发者决定账号标识改为邮箱、验证码经 SMTP 发到个人邮箱。设计 [email-verification.md](../../design/email-verification.md) 已采纳（第 8 节的 5 个问题均按推荐方案确认），顺带修复 TD-35、TD-36、TD-29：

- [x] 1. 接口：`SendEmailCode`、`LoginByEmailCode`，请求字段 `email`、`code`，枚举 `CodePurpose`，错误原因改名并新增 `DEPENDENCY_UNAVAILABLE`
- [x] 2. account：邮箱规范化与脱敏，Redis 键改名，用户缓存键 `account:user:v2:`，`smtp` 发送方（标准库，隐式 TLS 或 STARTTLS，截止时间不超过请求剩余时间，拒绝保留域名），假 SMTP 服务器单元测试
- [x] 3. 迁移 `000002_email`（开发库 up → down 1 → up 往返通过，553 行填入占位邮箱）；`grant-admin` 改为按邮箱
- [x] 4. 技术债：TD-35（发号失败返回 503，访问日志 503 记 WARN）、TD-36（go-redis 内部日志转到 `slog`）、TD-29（同意好友申请时检查拉黑）
- [x] 5. 文档：features、user-auth、contact、data-model、deployment、system、data-flow、account 与 social 设计、tech-debt
- [x] 6. 验证（2026-11-20）：`buf lint`、`make gen` 后无差异、`go vet`（含 `e2e`、`cluster` 标签）、`go build`、`go test -race ./...`（27 个包）通过；宿主机模式 e2e 32 项通过，五个服务日志无 ERROR；重建镜像后 `make cluster-test` 13 项通过（含 7 项故障实验），全部应用容器无 ERROR、无 go-redis 纯文本日志；Redis 停机 60 秒实验中需要发号的写操作全部返回 `503 DEPENDENCY_UNAVAILABLE`（实验新增了这条断言，单独重跑通过：18 次，两个 message 实例各 9 次）；`grant-admin` 手动验证成功（邮箱大小写混写）、邮箱不存在、非法输入三种情况。真实发信：容器模式经网关向开发者的邮箱发送注册码和登录码各一封（1.53 秒、1.18 秒），完成注册、密码登录和邮箱验证码登录，验证码一次性使用；首次按 465 端口发送因 TLS 被截获失败，改用 587，见 [lessons/smtp-465-tls-interception.md](../../lessons/smtp-465-tls-interception.md)

步骤 6 实施中（2026-11-20）：设计 [media-service.md](../../design/media-service.md) 已采纳。存储由设计推荐的 SeaweedFS 改为可切换的本地目录（默认）与阿里云 OSS，见 [ADR-0007](../../architecture/adr/0007-object-storage.md)；新增依赖 OSS 官方 SDK v2 与 `golang.org/x/image`（开发者已同意）。

步骤 6 第 1～5 段完成（2026-11-21），第 6 段（OSS 手动验证）等开发者提供 Bucket 与 AccessKey。实测数据：经网关上传 20 MiB 约 0.6 秒、下载约 0.14 秒（本地存储）；media 空闲约 18 MiB，并发上传 1590 万像素 16 位 PNG 时峰值 335 MiB（限制 384 MiB）。新增 TD-37（上传中途进程退出留下孤儿对象）、TD-38（缩略图不处理 JPEG 的 EXIF 方向）。验收时发现并修复 TD-39：Redis 停机约 20 秒后，节点号租约的下一轮续期可能晚于本地截止时间，留下短暂的发号失败窗口；`pkg/idgen` 改为续期失败后每秒重试。验证（2026-11-21，缩略图内存调整与 TD-39 修复之后）：`buf lint`、`make gen` 后无差异、`go vet`（含 `e2e`、`cluster` 标签）、`go build`、`go mod tidy -diff`、`go test -race ./...`（30 个包）通过；宿主机模式 `make e2e` 通过，六个服务日志无 ERROR；重建镜像后 `make cluster-test` 15 项通过（含 7 项故障实验）。修复前第一次运行时，`TestFaultStopRedisLong` 在准备阶段遇到上述窗口而失败，这也是发现 TD-39 的经过。第 1～5 段提交为 `f1ade90`。

步骤 7 完成（2026-11-21，coturn 按计划推迟），提交 `66ea720`。设计 [call-signaling.md](../../design/call-signaling.md) 已采纳（第 8 节的 7 个问题均按推荐方案确认），实施中的调整记在其第 9 节，主要一项是“先回复 `CallAck`，之后的失败一律经 `CallEnded` 通知”。验证：`buf lint`、`make gen` 两次结果一致、`go vet`（含 `e2e`、`cluster` 标签）、`go build`、`go mod tidy -diff`、`go test -race ./...`（30 个包）通过；宿主机模式 `make e2e` 通过（新增通话 3 项），六个服务日志无 ERROR；重建镜像后 `make cluster-test` 17 项通过，新增跨实例通话（offer 转发 0.5 毫秒）和通话中杀掉接听方 access 的故障实验（主叫下一次发信令 1 毫秒内收到 `DISCONNECTED`，忙线标记释放）。媒体能否连通要等前端重构后用浏览器验证。

步骤 8 完成（2026-11-21）。设计 [observability.md](../../design/observability.md) 已采纳（第 9 节的 8 个问题均按推荐方案确认），实施中的调整记在其第 10 节。按第 8 节分六段实施：

- [x] 1. 监控组件与指标基础：`pkg/telemetry`、配置段 `telemetry`、Kratos 指标中间件、各服务 `/metrics`、delivery 的 HTTP 端口 18040、Compose `monitoring` profile（全部 9 个应用实例在 Prometheus 中 `up`，监控组件空闲合计约 390 MiB）
- [x] 2. 业务指标、面板与告警：access、delivery、`pkg/kafka`、`pkg/outbox`、`pkg/cache`、`pkg/bloom`、`pkg/ratelimit` 的指标，kadm 积压，Grafana 总览面板（29 个面板，PromQL 全部能执行），8 条告警规则与 promtool 单元测试
- [x] 3. 链路追踪：OTLP 导出、tracing 中间件、WebSocket 帧的 Span、Outbox 迁移与 `traceparent` 传播、消费者 Span、日志带 `trace_id`（Jaeger 中一条消息跨 access、message、social、media、delivery 五个服务）
- [x] 4. MySQL 慢查询日志与集群验证（`SELECT SLEEP(0.3)` 写入慢查询日志；杀掉 access 实例触发 `InstanceDown`，重启后恢复）
- [x] 5. 压测：k6 脚本与 `make load-*`，三档 WebSocket 收发、热点读、击穿、穿透、积压告警实验，结果见[压测结果](#压测结果)。压测中发现并修复两个问题：并发注册死锁（TD-40）、连接池上限之和超过 MySQL 最大连接数（TD-41）；新发现 TD-42（delivery 全部停止后没有告警）、TD-43（并发登录仍可能在会话索引的间隙上死锁）
- [x] 6. 文档：system、deployment、data-model、data-flow、ADR-0005、tech-debt、设计第 10 节、lessons

验证（2026-11-21，全部修改之后）：`buf lint`、`make gen` 后无差异、`go vet`（含 `e2e`、`cluster` 标签）、`go build`、`go mod tidy -diff`、`go test -race ./...`（31 个包）、`make rules-test`（8 条规则）通过；宿主机模式 `make e2e` 通过，新增 `TestConcurrentRegister`（恢复旧的注册路径时 8 个并发注册有 2 个死锁，用例能发现问题）和 `TestMetricsEndpoints`；容器模式 `make cluster-test` 17 项通过（含 7 项故障实验）。验证结束后容器模式已按 `.env` 恢复（account 为 smtp 发送方，采样比例 1）。

核对代码时新发现 TD-34（节点号租约丢失后不会重新获取），列入本步骤修复。Compose 全栈内存已初步评估：Docker Desktop 虚拟机 3.8 GiB，现有占用约 2.1 GiB（Kafka 1.06 GiB），加上多实例与 APISIX 估计约 2.0 GiB（Kafka 堆限制为 512 MiB 后），第一阶段实测。

步骤 6 第 6 段（OSS 手动验证）完成（2026-11-21），提交 `452b2a9`。验证中发现并修复预签名地址覆盖 `Content-Type` 被 OSS 拒绝的问题；同时按开发者要求把 OSS 对象名改为按年、月分目录，整理了 `.env.example`。浏览器中的 `<img>` 显示与耗时在步骤 9 第 1 段补测。

步骤 9 完成（2026-11-21）。重写 `README.md`、`ARCHITECTURE.md`、`overview.md`，目标架构图改为当前架构（PNG 重新渲染）；按开发者要求删除旧基线描述；计划移到 `plans/completed/`。复核中补上了缺失的 `make dlq`。

## 故障实验结果

2026-11-20，容器模式（Docker Desktop），access、message、delivery 各 2 个实例，account、social、APISIX 各 1 个，代码见 `test/cluster/fault_test.go`，方法见[设计 3.9 节](../../design/gateway-multi-instance.md#39-故障实验)。

测试客户端按客户端约定处理断线：随机等待 0～2 秒后经网关重连，从本地连续的最大 seq 补拉，按 seq 去重。每个实验结束时都检查三件事：历史消息恰好是 1..N；`client_msg_id` 不重复；接收方经推送和补拉拿到的 seq 连续。下表 7 项实验全部满足，没有丢失和重复。数据取自修复后的完整运行（`make cluster-test`，13 项通过）；有多次运行数据的项目给出范围。计时从发出 `docker` 命令起算，包含命令本身的耗时（本机约 0.1～0.6 秒）。

| 实验 | 结果 |
|---|---|
| 跨节点收发 | 两个客户端连到不同 access 实例，跨实例推送约 1.6 ms；连续 10 次发送到推送 7.9～14.6 ms（第 3 段加入 Outbox 跨实例唤醒之前为 95～500 ms） |
| 杀掉 access | 被杀实例上的客户端以 1006 断开，2.3～3.7 秒后重连到另一实例（含 0～2 秒随机等待），补拉 6～9 条；发送没有失败 |
| 滚动重启 access | 每次重启（从 `docker restart -t 20` 到 Consul 中恢复 2 个 passing）1.5～2.8 秒；客户端都收到 1001，没有出现 1006；每次断线离线 0.6～1.8 秒；断线瞬间的发送由客户端用同一个 `client_msg_id` 重发后全部确认 |
| 杀掉 delivery | 推送最长中断 11.9～16.1 秒：会话超时 10 秒后协调者发起再均衡，存活实例经心跳得知后重新加入消费组，接管分区；中断期间的消息在分区转移后全部经推送到达 |
| 杀掉持有 Outbox 租约的 message | 另一个实例 8.0～11.1 秒后接手：租约 TTL 10 秒，从最后一次续期（每 3 秒一次）起算，非持有者每 0.5 秒尝试获取一次，再加上 kill 命令本身的耗时。推送最长延迟与接手耗时一致；没有重复推送 |
| 暂停 Kafka 30 秒 | 暂停期间 30 次发送全部确认（事件留在 Outbox）；恢复后 1.5～4.3 秒追平，按顺序推送 |
| 停掉 Redis 20 秒 | 停机期间 18～19 次发送全部确认（限流、吊销检查放行）；25 条中 22 条经推送到达（Redis 恢复后积压的事件发布），3 条只经补拉（停机期间 delivery 查不到在线路由） |
| 停掉 Redis 60 秒 | 已有会话的 62 次发送全部成功，它们不需要发号。需要发号的写操作（给新好友发第一条消息，要新建会话）最后一次成功在停机后 27.9 秒，第一次失败在 30.3 秒（节点号租约 TTL 30 秒），失败返回 `500 INTERNAL`（TD-35，2026-11-20 改为 `503 DEPENDENCY_UNAVAILABLE`）；两个 message 实例分别拒绝 8 次和 7 次。Redis 恢复后 3.8 秒恢复发号，两个 message 实例、account、social 各换用一次新节点号（TD-34 的修复）；恢复后注册和好友申请正常 |
| 通话中杀掉接听方的 access（2026-11-21，步骤 7，`TestFaultKillAccessDuringCall`） | 主叫与接听设备在不同实例上，通话中 `docker kill` 接听方所在实例。主叫在 kill 后 173 毫秒发出 ICE 信令，先收到 `CallAck`，1 毫秒内收到 `CallEnded{DISCONNECTED}`（到被杀实例的 gRPC 连接已断开，转发立即失败）；通话状态与双方忙线标记已删除；被叫经网关重连到存活实例后可以再次被呼叫 |

问题与处理：

- 跨实例推送延迟呈两极分布（第 2 段发现）：事务提交后的立即唤醒只在本进程有效。第 3 段加入经 Redis 发布订阅的跨实例唤醒。
- Redis 停机约 3 秒后发送开始失败（本段发现）：拨号把每条命令拖到几秒，见“遇到的问题”。修复前的数据：停机 20 秒的实验中，发送在 10 秒内没有得到确认（`503 UPSTREAM_UNAVAILABLE`）。
- 60 秒实验的预期写得不准：设计写的是“发号失败，写操作返回错误”，但 message 只有新建单聊会话时才发号，已有会话里发消息不受影响。实验改为分别验证这两类写操作。
- 新增 TD-35（发号失败返回 500 并记 ERROR）、TD-36（go-redis 内部日志是纯文本，故障期间每次拨号失败一行）。两项已在 2026-11-20 的邮箱验证码任务中解决。

## 压测结果

2026-11-21，容器模式。环境基线：

- Docker Desktop（Hyper-V 后端），虚拟机 24 个 CPU、3.8 GiB 内存；MySQL 9.7.4、Redis 8.8.4、Kafka 4.4.0（单节点）、APISIX 3.20.0；服务为步骤 8 的工作区代码（版本号 `66ea720-dirty`）。
- access、message、delivery 各 2 个实例，account、social、media、APISIX 各 1 个；应用容器内存限制 256 MiB（media 384 MiB），APISIX 512 MiB，k6 768 MiB。
- k6 2.4.0 经网关压测，`make load-up`：account 用 `log` 发送方，Trace 采样比例 0.05（对比实验除外）。
- 原始摘要在 `tmp/load/out/`（被 Git 忽略）：k6 的 `ws_chat_<连接数>.json`、`hot_read_*.json`，容器 CPU、内存与 Prometheus 指标的 `stats_<场景>.json`。

**准备数据**：经网关注册 1000 个用户（每批 40 个并发）、两两加好友、建一个 31 人的热点群，共 30.7 秒，注册约 35 个每秒（bcrypt cost 12，account 单实例）。第一次运行时 4 个并发注册有 3 个死锁，见“遇到的问题”。

**WebSocket 收发**（每个连接每秒给好友发 1 条单聊消息，全部连接建立后持续 60 秒；延迟只统计稳态窗口）：

| 连接数 | 发送确认 P50 / P99 / 最大 | 推送 P50 / P99 / 最大 | 发送 / 确认 / 推送 | 服务端投递 P99 | Outbox 发布延迟 P99 | 峰值 CPU：message 单实例、MySQL | 峰值内存：access 单实例、APISIX、k6 |
|---|---|---|---|---|---|---|---|
| 200 | 10 / 21 / 192 ms | 13 / 28 / 211 ms | 13043 / 13043 / 13043 | 27 ms | 25 ms | 29%、41% | 46、57、66 MiB |
| 500 | 12 / 107 / 208 ms | 18 / 124 / 230 ms | 32533 / 32533 / 32533 | 186 ms | 169 ms | 58%、95% | 65、66、100 MiB |
| 1000（MySQL 最大连接数 151） | 13 / 275 / 588 ms | 20 / 811 / 1220 ms | 65006 / 64843 / 64843，163 个错误帧 | 732 ms | 584 ms | 149%、270% | 86、71、185 MiB |
| 1000（最大连接数 300，最终配置） | 17 / 206 / 364 ms | 30 / 301 / 524 ms | 64988 / 64988 / 64988 | 249 ms | 239 ms | 109%、203% | 110、95、188 MiB |

- 全部档位没有意外断线，在线连接数（`marschat_ws_connections` 之和）达到目标值，Kafka 积压最大 60 条。
- **瓶颈在写路径**：推送延迟的 P99 几乎都花在 Outbox 发布延迟上（写库事务提交到发布器发出），1000 个连接时 MySQL 约占 2 个 CPU、message 每个实例约 1 个 CPU；access、delivery、APISIX 的 CPU 都在 60% 以下。
- **1000 个连接的第一次运行有 0.25% 的发送失败**：原因是 MySQL `Error 1040: Too many connections`，各服务连接池上限之和为 230，超过默认的 151，峰值实际用到 152 条。先尝试把连接池缩小到合计 120（message 每实例 30），结果更差：message 拿不到连接的请求排队到超时，access 的熔断器打开，2.7 万次发送失败（`CIRCUITBREAKER` 2.3 万次、`UPSTREAM_UNAVAILABLE` 4 千次），确认 P99 19 秒。最终恢复原来的连接池，把 MySQL 最大连接数调到 300，峰值用到 158 条，没有错误（TD-41）。
- **内存**：access 每 500 条连接增加约 65 MiB（每条约 130 KiB，主要是读写缓冲和发送队列）；k6 每 1000 条连接约 190 MiB，没有成为上限；MySQL 峰值 563 MiB。
- **采样比例的影响**（500 个连接，最大连接数 300）：采样 0.05 时确认 / 推送 P99 为 146 / 201 ms，关闭时为 125 / 159 ms，服务端投递 P99 分别为 241、236 ms。差异在两次运行之间的正常波动范围内（同一配置先后两次的 P99 相差约 40 ms），压测用 0.05 不会明显扭曲结果。采样 1.0 在每秒几百条消息时会超出 Jaeger 内存存储的容量，没有测。

**热点读**（固定 500 次每秒、60 秒，调用方为热点群的 31 个成员）：

| 接口 | P99 | 缓存 |
|---|---|---|
| 用户资料 `GET /v1/users/{id}` | 1.27 ms | `account_user` 全部命中 |
| 群资料 `GET /v1/groups/{id}` | 1.67 ms | `social_group` 命中；成员资格检查直接查 MySQL |
| 群成员 `GET /v1/groups/{id}/members` | 2.84 ms | 分页列表直接查 MySQL，补全资料走 `account_user` 缓存 |
| 会话列表 `GET /v1/conversations` | 4.05 ms | 直接查 MySQL |

30001 个请求全部成功。MySQL 约 1000 次查询每秒、CPU 17%，主要来自成员资格检查、成员列表和会话列表；social CPU 29%、account 18%。群资料和成员列表在这个规模下不需要更多缓存，读量再大时成员资格检查是第一个要考虑缓存的地方。

**击穿**：热点读进行到第 31 秒时删除热点群的缓存键（`make load-bust`）。之后 15 秒内 `social_group` 只有 2 次未命中、1 次回源（singleflight 合并了本实例的并发回源），群资料 P99 仍为 1.67 ms、最大 6 ms。social 只有一个实例，跨实例重建锁没有参与；开启重建锁的群成员缓存只在群消息与投递中使用，这个场景不读它。

**穿透**（不存在的用户 ID 和群 ID，每个 ID 只用一次，500 次每秒、30 秒）：15001 个请求全部返回 404，P99 1.5 ms；布隆过滤器拒绝 7501 + 7500 次。实验窗口（约 42 秒）内 MySQL 的 `Com_select` 增加 268，与空闲时的约 7 次每秒（后台轮询）相当，穿透请求没有到达数据库。每个 ID 仍会写一个 60 秒的空值标记，500 次每秒的随机 ID 约占 3 万个键。

**积压告警**：停止两个 message 实例后发 2000 个好友申请，消费组 `marschat-message` 在 `marschat.social.events` 上积压 2000 条。`KafkaConsumerLag` 13:03:42 进入 pending，13:08:42 触发并到达 Alertmanager（持续 5 分钟的条件）；启动 message 后 16 秒内积压归零、告警结束。积压由 delivery 统计，所以不能用停止 delivery 的方式实验（TD-42）。

结论：这台机器上 1000 个在线连接、约每秒 1000 条消息时，推送 P99 约 300 ms、没有错误，瓶颈是 MySQL 和 message 的写路径。结果只作为对比基线，不代表生产容量。

## 遇到的问题

- 2026-11-18：`docker compose up` 创建 MySQL 容器时一直卡住。原因是本机 Docker Desktop 使用 Hyper-V 后端，挂载宿主机目录（`deploy/mysql/init`）前要在 File sharing 中授权，未授权时 CLI 会等待弹窗确认。Redis、Kafka、Consul 只用命名卷，不受影响。在 Docker Desktop 中授权共享后 MySQL 正常初始化；建议把项目根目录加入 File sharing，已写入 [deployment.md](../../deployment.md#首次配置) 和 [lessons/docker-desktop-bind-mount-hang.md](../../lessons/docker-desktop-bind-mount-hang.md)。
- 2026-11-18：配置中的时长写成 `500ms`、`1h` 时解析失败。Kratos 用 protojson 解析 `google.protobuf.Duration`，只接受以秒为单位的写法（`0.5s`、`3600s`）。已改正配置，并为每个服务加了解析真实配置文件的单元测试。
- 2026-11-18：`make e2e` 通过服务发现调用时 panic（`transport/grpc/balancer.go:52` 空指针）。Kratos v3.0.0 的 gRPC 负载均衡器在包初始化时就存下了全局选择器，默认选择器设置得更晚，存下的是 nil。新增 `pkg/balancer` 利用 Go 的包初始化顺序提前设置选择器，见 [lessons/kratos-grpc-selector-nil.md](../../lessons/kratos-grpc-selector-nil.md)。升级 Kratos 时检查上游是否已修复。
- 2026-11-18：`make run-account` 报 `unexpected EOF while looking for matching '"'`。GnuWin32 make 3.81 按 GBK 代码页把命令行传给 sh，echo 中文提示的 UTF-8 字节和相邻引号组成了非法字符，引号丢失。Makefile 的命令行改为只用 ASCII，并加了注释。
- 2026-11-18：account 的 HTTP 接口不接受枚举名字、int64 输出为数字。Kratos v3 的 `json` codec 改为纯标准库实现，不再按 protobuf JSON 处理 proto 消息。新增 `pkg/jsoncodec` 覆盖注册，见 [lessons/kratos-v3-server-defaults.md](../../lessons/kratos-v3-server-defaults.md)。
- 2026-11-18：Kratos 自带的 `logging` 中间件会把请求体（含密码、验证码、刷新令牌）写进日志，`recovery` 记录 panic 时也打印请求体；非 Kratos 错误的原文会返回给客户端。改用自己实现的 `pkg/middleware/accesslog`，见同一篇 lesson。
- 2026-11-20：真实发信时 465 端口报 `certificate signed by unknown authority`。开发机到 `smtp.qq.com:465`、`smtp.163.com:465` 的 TLS 连接被截获，拿到的是名为 `Public Certificate` 的自签证书，587（STARTTLS）与 HTTPS 正常。改用 587，见 [lessons/smtp-465-tls-interception.md](../../lessons/smtp-465-tls-interception.md)。
- 2026-11-19：补 TD-27 的布隆过滤器集成测试时发现，`BF.EXISTS` 在 go-redis v9 默认的 RESP3 下返回布尔值，原代码按整数读取，读取失败后放行，account 的用户 ID 过滤器从步骤 2 起没有拦截过任何 ID。已改用 `Cmd.Bool()` 并在解析失败时记 WARN，见 [lessons/redis-resp3-reply-types.md](../../lessons/redis-resp3-reply-types.md)。

- 2026-11-20：Redis 故障实验中，停掉 Redis 约 3 秒后发消息开始失败，而设计预期 20 秒内照常成功。容器停止后服务名从 Docker DNS 中消失，每次解析约 1 秒才失败；go-redis v9.23 默认单次拨号重试 5 次、命令重试 3 次，一条命令就用完了调用方的截止时间，限流的“失败放行”来不及生效。`pkg/bootstrap.NewRedis` 加拨号保护并关闭单次拨号内的重试，见 [lessons/redis-dial-stall-docker-dns.md](../../lessons/redis-dial-stall-docker-dns.md)。
- Compose 全栈（Kafka、MySQL、Redis、Consul、APISIX、监控组件加多实例服务；2026-11-20 起不再部署 MinIO）在 Docker Desktop 上的内存占用：步骤 5 的全部容器实测约 1.2～1.3 GiB（见 [ADR-0005](../../architecture/adr/0005-distributed-architecture.md#代价与风险)），步骤 6 加入 media 后空闲时合计约 1.2 GiB（2026-11-21，media 约 18 MiB）。步骤 8 实测（2026-11-21）：监控组件空闲约 390 MiB，跑完集群测试与压测后约 510 MiB（Jaeger 随保存的 Trace 增长到约 170 MiB）；接入遥测后应用服务各增加约 15～25 MiB（例如 access 约 21 → 38 MiB、account 约 19 → 45 MiB）；MarsChat 全部容器（含监控组件）空闲时合计约 1.6 GiB，1000 条连接压测时各容器峰值之和约 2.8 GiB（含 k6，峰值不一定同时出现），离 3.8 GiB 的虚拟机上限约有 1 GiB 余量。
- 2026-11-21：压测准备数据时并发注册死锁（`Error 1213`）。注册事务中吊销设备旧会话的 `SELECT ... FOR UPDATE` 查不到行，锁住了会话唯一索引末尾的间隙，新用户都落在这个间隙。注册改为不做这次查询（TD-40），见 [lessons/innodb-gap-lock-register-deadlock.md](../../lessons/innodb-gap-lock-register-deadlock.md)。
- 2026-11-21：网关按客户端 IP 限流（WebSocket 每个 IP 最多 50 条连接），k6 在单个容器里只有一个来源地址，200 个连接以上会被网关拒绝，设计没有考虑到。k6 改用固定 IP，网关只对它信任 `X-Forwarded-For`（`real-ip` 全局规则），每个虚拟客户端带不同的地址，限流按虚拟客户端生效；准备数据也因此改在 k6 中完成（网关对 `/v1/auth/*` 每个 IP 每分钟只放 60 次，从宿主机注册 1000 个用户要半小时以上）。见设计第 10 节。
- 2026-11-21：1000 个连接时 MySQL 报 `Error 1040: Too many connections`，各服务连接池上限之和超过最大连接数（TD-41）。缩小连接池反而让请求排队超时、触发熔断，最终调大 MySQL 最大连接数，见[压测结果](#压测结果)。
