# MarsChat 架构总览

后端由六个 Kratos v3 微服务组成，接口用 protobuf 定义，同一份 proto 生成 HTTP 与 gRPC。客户端经 APISIX 网关访问：HTTP 接口按路径直达业务服务，WebSocket 只连接入服务 access。消息按会话存一份（读扩散），写库与发布事件在同一事务中（Outbox），经 Kafka 由 delivery 定向推送到持有连接的 access 实例；推送尽力而为，断线后按会话 seq 补拉。前端在 `web/` 用 React + Vite + pnpm 重写，已接入认证（登录、注册、令牌刷新、退出），其他功能尚未接入后端，见 [ADR-0008](docs/architecture/adr/0008-react-frontend.md) 和[前端计划](docs/plans/active/frontend-rewrite.md)。

![当前架构](docs/images/proposed-distributed-architecture.svg)

选型理由见 [ADR-0005](docs/architecture/adr/0005-distributed-architecture.md)（文件存储见 [ADR-0007](docs/architecture/adr/0007-object-storage.md)），同步模型见 [ADR-0006](docs/architecture/adr/0006-message-sync-model.md)，代码结构与横切约定见 [system.md](docs/architecture/system.md)。

## 服务

| 服务 | 职责 | 数据 | 容器模式实例数 |
|---|---|---|---|
| access | WebSocket 接入：首帧认证、心跳、在线路由、慢连接隔离；转发发送请求给 message；一对一通话信令 | Redis（在线路由、通话状态） | 2 |
| account | 邮箱验证码、注册登录、访问令牌与刷新令牌、资料、设备、封禁 | MySQL `marschat_account`、Redis | 1 |
| social | 好友、申请、拉黑、群与成员角色；发言权限校验 | MySQL `marschat_social`、Redis | 1 |
| message | 发送（幂等、会话内 seq）、会话列表与未读数、历史、补拉、已读位置；群会话成员投影 | MySQL `marschat_message`、Redis | 2 |
| delivery | 消费消息、账号与社交事件，按在线路由推送或踢连接 | Redis（在线路由、去重） | 2 |
| media | 上传、缩略图、下载地址；给 message 核实文件 | MySQL `marschat_media`、本地目录或阿里云 OSS | 1 |

依赖组件：MySQL（按服务分库分账号）、Redis（缓存、限流、租约、在线路由）、Kafka（事件分发）、Consul（服务注册发现，APISIX 也从这里发现上游）、APISIX（TLS、入口限流、路由）。监控组件 Prometheus、Alertmanager、Grafana、Jaeger 与压测工具 k6 在单独的 Compose profile 中。端口与启动方式见 [deployment.md](docs/deployment.md)。

## 关键数据流

1. **HTTP 请求**：网关 → 业务服务的中间件链（链路追踪、指标、访问日志、令牌校验、参数校验）→ service → biz → data。错误统一为 Kratos 错误（HTTP 状态码 + `reason`）。
2. **发送消息**：客户端经 WebSocket 发 `SendMessage` 帧 → access 调 message → message 校验发言权限（social），在一个事务中分配 seq、写消息、写 Outbox → 回复 `SendAck`。`client_msg_id` 唯一约束保证重发只存一条。
3. **推送与同步**：Outbox 发布器把事件写入 Kafka（按会话分区）→ delivery 查在线路由，按 access 实例分组直连推送，排除发起连接 → 客户端用 seq 检查连续性，有缺口时调用 `GetDifference` 补拉。
4. **好友与群通知、踢下线**：social、account 的事件经同样的 Outbox 与 Kafka 到 delivery，推送 `Notification` 帧或断开连接。
5. **文件**：客户端先上传到 media 拿到文件 ID 和访问密钥，消息只引用文件；下载时换取 10 分钟有效的签名地址，本地存储由 media 校验签名后返回，OSS 用预签名地址直连。
6. **通话信令**：access 校验好友关系后，按在线路由把邀请、接听、SDP、ICE 直接转给对端所在的 access，不落库、不经 Kafka；媒体走 WebRTC 点对点。

各流程的细节与 WebSocket 帧协议见 [data-flow.md](docs/architecture/data-flow.md)，表与 Redis 键见 [data-model.md](docs/architecture/data-model.md)。

## 可靠性与扩展

| 问题 | 做法 | 详见 |
|---|---|---|
| 写库与发事件不一致 | 事务 Outbox；每个服务由持有 Redis 租约的实例发布，保持同一个键的顺序 | [message-pipeline.md](docs/design/message-pipeline.md) |
| 至少一次投递下重复处理 | 消费幂等（SQL 幂等、按事件 ID 去重）；失败重试，处理不了的进死信队列 | [data-flow.md](docs/architecture/data-flow.md#6-事件与-outbox) |
| 多实例下推送到正确的连接 | Redis 在线路由（用户 → 设备 → 实例），心跳续期 TTL | [gateway-multi-instance.md](docs/design/gateway-multi-instance.md) |
| 推送丢失、断线 | 推送尽力而为，客户端按 seq 补拉（`COMPLETE`、`SLICE`、`TOO_LONG`） | [ADR-0006](docs/architecture/adr/0006-message-sync-model.md) |
| 依赖故障时雪崩 | 超时、幂等调用重试、熔断；缓存防穿透（布隆过滤器、空值）、防击穿（重建锁）、防雪崩（TTL 抖动） | [system.md](docs/architecture/system.md) |
| 刷消息、刷验证码 | 网关按 IP 限流；Redis 滑动窗口按用户限流；验证码发送间隔与每日上限 | [account-service.md](docs/design/account-service.md) |
| 发布与重启不丢消息 | access 停机时先从 Consul 注销，再分批关闭连接通知客户端重连 | [data-flow.md](docs/architecture/data-flow.md#7-停机) |
| 定位延迟与故障 | 指标与告警、链路追踪（跨 Kafka）、慢查询日志；故障实验与压测验证 | [observability.md](docs/design/observability.md) |

故障实验与压测结果见 [后端重构计划](docs/plans/completed/backend-refactor.md#故障实验结果)。

## 主要约束

- 前端只接入了认证，实时消息、联系人、文件、通话等仍在重写中；音视频的 TURN 中继（coturn）、ICE 服务器下发和群通话随前端重构实现。
- 服务间调用没有认证，依赖内网隔离（TD-26）；media 只有一个实例，多实例需要共享存储目录或使用 OSS。
- Compose 是唯一的部署方式，组件本身不做高可用；压测数据只作为这台开发机上的对比基线。
- 已知问题见 [tech-debt.md](docs/plans/tech-debt.md)。

## 详细文档

| 主题 | 文档 |
|---|---|
| 分层、目录、技术组件、横切约定 | [docs/architecture/system.md](docs/architecture/system.md) |
| 表结构、Redis 键 | [docs/architecture/data-model.md](docs/architecture/data-model.md) |
| 请求、消息、事件、信令的流转与帧格式 | [docs/architecture/data-flow.md](docs/architecture/data-flow.md) |
| 各服务的设计与取舍 | [docs/design/](docs/design/) |
| 架构决策记录 | [docs/architecture/adr/](docs/architecture/adr/README.md) |
| 部署与配置 | [docs/deployment.md](docs/deployment.md) |
| 功能与实现状态 | [docs/product/features.md](docs/product/features.md) |
| 已知问题 | [docs/plans/tech-debt.md](docs/plans/tech-debt.md) |
