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

应用服务按 HTTP `18xx0`、gRPC `19xx0` 编排（2026-11-18 确认）：access 18000（WebSocket）、account 18010 / 19010、social 18020 / 19020、message 18030 / 19030、delivery 19040（仅 gRPC）、media 18050 / 19050。

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
| Outbox 多实例发布（`FOR UPDATE SKIP LOCKED`） | 多个 message 实例并发发布时不重复、不加分布式锁 | 5 |
| Redis 在线路由 + 定向推送 | WebSocket 多实例下跨节点投递 | 4、5 |
| 缓存三类问题防护（见上节） | 穿透、击穿、雪崩 | 1、2、3 |
| 限流（网关入口 + Redis 按用户滑动窗口） | 防刷消息、防短信轰炸 | 2、4、5 |
| 短信防刷（发送间隔、每日上限、按 IP 限流、验证次数上限、一次性使用） | 短信成本与暴力破解 | 2 |
| JWT + 刷新令牌轮换 + Redis 吊销名单 | 无状态鉴权，同时支持退出、封禁即时失效 | 2 |
| snowflake 节点号用 Redis 租约分配 | 多实例生成 ID 不冲突 | 1 |
| protovalidate 参数校验 | 在接口契约中声明校验规则 | 1 |
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

- [ ] 新 `compose.yaml`：MySQL、Redis、Kafka（KRaft）、Consul，含健康检查和 topic 初始化。
- [ ] 单 Go module 的 monorepo（module 路径 `github.com/Mars-08042/MarsChat`）：`api/<服务>/v1/*.proto`；`app/<服务>/` 按 Kratos v3 分层（`cmd`、`internal/{biz,data,service,server,conf}`、`configs`）；跨服务公共代码放 `pkg/`。
- [ ] 用 buf 管理 proto：lint、breaking 检查，生成 gRPC、HTTP、errors 和 OpenAPI 代码；接入 protovalidate；生成命令能在 Git Bash 下运行。
- [ ] 公共能力：错误码、配置加载与环境变量覆盖、`slog` 日志、Consul 注册发现、OpenTelemetry 中间件、snowflake ID（节点号用 Redis 租约分配）、GORM 数据访问、按服务分库的 SQL 迁移（用 `migrate/migrate` 镜像执行）、带三类防护的缓存组件、Redis 限流组件。
- [ ] 确认 Kratos v3 的 Consul 注册组件、JWT 组件和参数校验的接入方式（v3 已把 JWT、OpenTelemetry 拆到 contrib 模块）。
- [ ] GitHub Actions：buf lint、go vet、单测、构建（推送后才会运行，推送需开发者同意）。
- [ ] 验证：`buf lint`、`go vet ./...`、`go build ./...`；两个空服务注册到 Consul 后能互相调用。

### 2. account 服务

- [ ] 手机号加短信验证码注册；密码登录与短信验证码登录；刷新令牌、退出、个人资料、设备列表与踢设备、管理员封禁。
- [ ] 短信：阿里云短信，开发环境用只写日志的假实现；验证码存 Redis，加上发送间隔、每日上限、按 IP 限流、验证次数上限，验证后立即失效。
- [ ] 密码用 bcrypt 哈希；JWT 访问令牌加按设备签发的刷新令牌（使用后轮换）；退出和封禁时写入 Redis 吊销名单；提供公共 JWT 中间件给其他服务复用。
- [ ] 用户资料缓存接入缓存组件；封禁和踢设备时经 Outbox 发出事件，供 access 断开连接（步骤 4 接入）。
- [ ] 验证：单元测试；HTTP 接口冒烟测试；短信限流用例。

### 3. social 服务

- [ ] 好友：申请、同意、拒绝、删除、拉黑。群组：创建、直接加入或审核加入、成员与角色、退出、解散、禁言。
- [ ] 对内 gRPC：发言权限校验（给 message 用）、群成员列表（给 delivery 用，接入缓存组件）。
- [ ] 群资料修改用乐观锁；建群、入群、退群经 Outbox 发事件，由 message 创建或更新会话（跨服务最终一致）。
- [ ] 以旧模块文档 [contact.md](../../product/modules/contact.md)、[group.md](../../product/modules/group.md) 为功能基线，同时修正 TD-16、TD-17 中的逻辑错误。
- [ ] 验证：单元测试；HTTP 接口冒烟测试。

### 4. 核心消息链路（各服务单实例）

- [ ] access：WebSocket 接入、握手校验令牌、心跳、每连接有界发送队列（满了断开慢连接）、帧协议用 proto 定义、Redis 在线路由（用户 → 设备 → 实例，TTL 随心跳续期）、按用户限制发送频率、收到封禁事件断开连接。
- [ ] message：会话模型（读扩散）、按 `client_msg_id` 幂等（唯一约束）、事务内用行锁分配会话 seq、消息与 Outbox 同事务、历史游标分页、会话列表（最后一条消息、未读数）、已读位置、按游标补拉（完整、分批、缺口过大重载三种结果）。
- [ ] Outbox 发布器与 Kafka（按会话分区）；delivery 最小实现：消费事件（幂等）、查在线路由、推送给在线设备并排除发起设备。
- [ ] 在 `test/e2e` 编写端到端测试（Go WebSocket 客户端），覆盖：发送确认、重复发送只存一条、断线补齐、多端同步、已读与未读。

### 5. 网关与多实例

- [ ] APISIX Standalone：HTTP 按路径直达各服务（通过 Consul 发现），WebSocket 转发到 access，TLS 与入口限流。先验证 Standalone 与 Consul 发现能组合使用（官方文档未见组合示例）。
- [ ] 服务镜像与 Compose 多实例（access、message、delivery 各起 N 个）；Outbox 多实例发布用 `FOR UPDATE SKIP LOCKED`。
- [ ] delivery 完整实现：按路由直连目标 access 实例并缓存连接、群成员扩散、推送尽力而为；消费失败进重试队列，多次失败进死信队列。
- [ ] 服务间调用加超时、仅对幂等调用重试、熔断；access 优雅停机（通知客户端重连、先从 Consul 摘除）。
- [ ] 故障实验：跨节点收发；杀掉 access 或 delivery 实例；滚动重启 access；暂停 Kafka；停掉 Redis 验证降级。验证不丢、不重、重连能补齐，结果记录在本计划。

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

步骤 0 已完成（2026-11-18）。旧的本地配置（含阿里云短信 AccessKey）未保留，步骤 2 接入短信时重新配置。下一步：步骤 1，搭建基础设施与工程骨架。

## 遇到的问题

- Compose 全栈（Kafka、MySQL、Redis、Consul、APISIX、MinIO、监控组件加多实例服务）在 Docker Desktop 上的内存占用未实测，步骤 5 前评估，必要时按步骤分组启动。
