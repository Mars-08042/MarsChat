# 计划：后端重构

- 状态：进行中 | 开始：2026-11-18 | 分支：`dev` | 关联设计：[目标架构图](../../images/proposed-distributed-architecture.svg)、[ADR-0005](../../architecture/adr/0005-distributed-architecture.md)、[ADR-0006](../../architecture/adr/0006-message-sync-model.md)

## 目标

在 `dev` 分支上用 Kratos v3 重写后端：按目标架构拆分为 access、account、social、message、delivery、media 六个服务，接口全部用 protobuf 定义。实现可靠消息（幂等、会话内有序、事务 Outbox、断线补拉）、多端同步、跨节点投递和慢连接隔离，并用端到端测试、故障实验和压测验证。

项目用于求职展示，适合本项目的生产环境技术会引入，每项都要对应一个实际要解决的问题（见“生产环境技术点”）。

前端重构另立计划（React + pnpm），本计划只负责移除旧的 Vue 前端。

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
| MinIO（API / 控制台） | 9000 / 9001 | 19000 / 19001 | 6 |
| Prometheus | 9090 | 19090 | 8 |
| Alertmanager | 9093 | 19093 | 8 |
| Grafana | 3000 | 13000 | 8 |
| Jaeger（UI / OTLP gRPC / OTLP HTTP） | 16686 / 4317 / 4318 | 26686 / 14317 / 14318 | 8 |

应用服务按 HTTP `18xx0`、gRPC `19xx0` 编排（2026-11-18 确认）：access 18000（WebSocket）/ 19100（内部 gRPC，19000 留给 MinIO，2026-11-19 确认）、account 18010 / 19010、social 18020 / 19020、message 18030 / 19030、delivery 19040（仅 gRPC）、media 18050 / 19050。

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
- [x] 手机号加短信验证码注册；密码登录与短信验证码登录；刷新令牌、退出、个人资料、设备列表与踢设备、管理员封禁与解封。
- [ ] 短信：开发环境只写日志的发送方已实现；验证码存 Redis，加上发送间隔、每日上限、按 IP 限流、验证次数上限，验证后立即失效，限额可热更新。**阿里云短信未实现**（没有 AccessKey，TD-24）。
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

- [ ] MinIO（S3 兼容）存储；上传时由服务端生成文件 ID 并限制大小和类型；下载前校验权限；文件消息引用文件 ID；图片生成缩略图。

### 7. 通话信令

- [ ] 信令帧（邀请、接听、拒绝、挂断、SDP、ICE）走 WebSocket；access 按在线路由直转对端实例，不落库、不经 Kafka；对方离线时立即返回失败；多端来电，一端接听后其他端提示“已在其他设备接听”。
- [ ] coturn 推迟到前端重构时部署：TURN 中继端口段难以套用 +10000 规则，届时再单独确认。

### 8. 可观测与压测

- [ ] Prometheus、Grafana、Alertmanager；指标包括连接数、消息端到端延迟、推送失败数、Kafka 积压、缓存命中率。
- [ ] OpenTelemetry + Jaeger，消息 ID 贯穿日志与 Trace；开启 MySQL 慢查询日志。
- [ ] 用 k6（Docker 镜像运行）压测 WebSocket 收发和热点读接口，记录在线连接数、P99 延迟和缓存防护效果。

### 9. 收尾

- [ ] 复核 `ARCHITECTURE.md`、`system.md`、`data-model.md`、`data-flow.md`、`deployment.md`、`features.md` 和模块文档；本计划补上结果后移到 `plans/completed/`。

### 10. 可选扩展（主线完成后再定）

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
| 6 | 文件服务与 MinIO 初始化 | `app/service/dfs/`、`app/service/media/`、`minio_init.sh` | — |
| 8 | Prometheus、日志收集、链路追踪 | `teamgramd/deploy/prometheus/`、`docs/log-collection-zh.md`、`docs/link-tracking-zh.md` | 我们用 OpenTelemetry + Jaeger |

不参考：MTProto 协议层、写扩散与按用户分表、独立的 idgen 服务、12 个服务的拆分粒度。teamgram 写库与投递 Kafka 不在同一事务中，pts 记录写失败也只打日志，我们用事务 Outbox 避免这两个问题。

## 已确认的决定（2026-11-18）

- 保留手机号加短信验证码注册，以及短信验证码登录。
- 应用服务端口按 HTTP `18xx0`、gRPC `19xx0` 编排。
- 数据库表按新项目重新设计，考虑事务、索引和缓存三类问题。
- 适合本项目的生产环境技术可以引入。

## 当前进度与下一步

步骤 0 已完成（2026-11-18）。旧的本地配置（含阿里云短信 AccessKey）未保留，步骤 2 接入短信时重新配置。

步骤 1 已完成（2026-11-18），提交 `6685aae`。

步骤 2 已完成（2026-11-18），提交 `bfe2778`，阿里云短信除外（TD-24）。设计 [account-service.md](../../design/account-service.md) 已采纳，实施中的调整记在其第 8 节。验证：`buf lint`、`go vet`、`go build`、`go test -race ./...`、`make e2e`（6 个用例）通过，关键查询 `EXPLAIN` 已核对。

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

步骤 5 已完成（2026-11-20），提交 `1f8e77e`（第 1～3 段）、`52eb184`（第 4 段）及第 5 段的提交。实施中的调整记在设计第 9 节。验证：`buf lint`、`go vet`（含 `e2e`、`cluster` 标签）、`go build`、`go test -race ./...`、宿主机模式 `make e2e`、容器模式 `make cluster-test` 通过。

下一步：步骤 6（media 服务）。开始前先写设计文档，重点是 MinIO 的部署与端口、上传大小和类型限制、下载权限校验、缩略图处理（可复用第 4 段的重试队列与死信队列）。

核对代码时新发现 TD-34（节点号租约丢失后不会重新获取），列入本步骤修复。Compose 全栈内存已初步评估：Docker Desktop 虚拟机 3.8 GiB，现有占用约 2.1 GiB（Kafka 1.06 GiB），加上多实例与 APISIX 估计约 2.0 GiB（Kafka 堆限制为 512 MiB 后），第一阶段实测。

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
| 停掉 Redis 60 秒 | 已有会话的 62 次发送全部成功，它们不需要发号。需要发号的写操作（给新好友发第一条消息，要新建会话）最后一次成功在停机后 27.9 秒，第一次失败在 30.3 秒（节点号租约 TTL 30 秒），失败返回 `500 INTERNAL`（TD-35）；两个 message 实例分别拒绝 8 次和 7 次。Redis 恢复后 3.8 秒恢复发号，两个 message 实例、account、social 各换用一次新节点号（TD-34 的修复）；恢复后注册和好友申请正常 |

问题与处理：

- 跨实例推送延迟呈两极分布（第 2 段发现）：事务提交后的立即唤醒只在本进程有效。第 3 段加入经 Redis 发布订阅的跨实例唤醒。
- Redis 停机约 3 秒后发送开始失败（本段发现）：拨号把每条命令拖到几秒，见“遇到的问题”。修复前的数据：停机 20 秒的实验中，发送在 10 秒内没有得到确认（`503 UPSTREAM_UNAVAILABLE`）。
- 60 秒实验的预期写得不准：设计写的是“发号失败，写操作返回错误”，但 message 只有新建单聊会话时才发号，已有会话里发消息不受影响。实验改为分别验证这两类写操作。
- 新增 TD-35（发号失败返回 500 并记 ERROR）、TD-36（go-redis 内部日志是纯文本，故障期间每次拨号失败一行）。

## 遇到的问题

- 2026-11-18：`docker compose up` 创建 MySQL 容器时一直卡住。原因是本机 Docker Desktop 使用 Hyper-V 后端，挂载宿主机目录（`deploy/mysql/init`）前要在 File sharing 中授权，未授权时 CLI 会等待弹窗确认。Redis、Kafka、Consul 只用命名卷，不受影响。在 Docker Desktop 中授权共享后 MySQL 正常初始化；建议把项目根目录加入 File sharing，已写入 [deployment.md](../../deployment.md#首次配置) 和 [lessons/docker-desktop-bind-mount-hang.md](../../lessons/docker-desktop-bind-mount-hang.md)。
- 2026-11-18：配置中的时长写成 `500ms`、`1h` 时解析失败。Kratos 用 protojson 解析 `google.protobuf.Duration`，只接受以秒为单位的写法（`0.5s`、`3600s`）。已改正配置，并为每个服务加了解析真实配置文件的单元测试。
- 2026-11-18：`make e2e` 通过服务发现调用时 panic（`transport/grpc/balancer.go:52` 空指针）。Kratos v3.0.0 的 gRPC 负载均衡器在包初始化时就存下了全局选择器，默认选择器设置得更晚，存下的是 nil。新增 `pkg/balancer` 利用 Go 的包初始化顺序提前设置选择器，见 [lessons/kratos-grpc-selector-nil.md](../../lessons/kratos-grpc-selector-nil.md)。升级 Kratos 时检查上游是否已修复。
- 2026-11-18：`make run-account` 报 `unexpected EOF while looking for matching '"'`。GnuWin32 make 3.81 按 GBK 代码页把命令行传给 sh，echo 中文提示的 UTF-8 字节和相邻引号组成了非法字符，引号丢失。Makefile 的命令行改为只用 ASCII，并加了注释。
- 2026-11-18：account 的 HTTP 接口不接受枚举名字、int64 输出为数字。Kratos v3 的 `json` codec 改为纯标准库实现，不再按 protobuf JSON 处理 proto 消息。新增 `pkg/jsoncodec` 覆盖注册，见 [lessons/kratos-v3-server-defaults.md](../../lessons/kratos-v3-server-defaults.md)。
- 2026-11-18：Kratos 自带的 `logging` 中间件会把请求体（含密码、验证码、刷新令牌）写进日志，`recovery` 记录 panic 时也打印请求体；非 Kratos 错误的原文会返回给客户端。改用自己实现的 `pkg/middleware/accesslog`，见同一篇 lesson。
- 2026-11-19：补 TD-27 的布隆过滤器集成测试时发现，`BF.EXISTS` 在 go-redis v9 默认的 RESP3 下返回布尔值，原代码按整数读取，读取失败后放行，account 的用户 ID 过滤器从步骤 2 起没有拦截过任何 ID。已改用 `Cmd.Bool()` 并在解析失败时记 WARN，见 [lessons/redis-resp3-reply-types.md](../../lessons/redis-resp3-reply-types.md)。

- 2026-11-20：Redis 故障实验中，停掉 Redis 约 3 秒后发消息开始失败，而设计预期 20 秒内照常成功。容器停止后服务名从 Docker DNS 中消失，每次解析约 1 秒才失败；go-redis v9.23 默认单次拨号重试 5 次、命令重试 3 次，一条命令就用完了调用方的截止时间，限流的“失败放行”来不及生效。`pkg/bootstrap.NewRedis` 加拨号保护并关闭单次拨号内的重试，见 [lessons/redis-dial-stall-docker-dns.md](../../lessons/redis-dial-stall-docker-dns.md)。
- Compose 全栈（Kafka、MySQL、Redis、Consul、APISIX、MinIO、监控组件加多实例服务）在 Docker Desktop 上的内存占用：步骤 5 的全部容器实测约 1.2～1.3 GiB（见 [ADR-0005](../../architecture/adr/0005-distributed-architecture.md#代价与风险)），MinIO 与监控组件在步骤 6、8 引入时再测。
