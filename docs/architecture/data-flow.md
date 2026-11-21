# 数据流

> 描述请求、消息和事件在服务之间如何流转，WebSocket 帧协议以本文为准，最近一次更新于 2026-11-21（后端重构完成）。服务内分层见 [system.md](system.md)，表与缓存键见 [data-model.md](data-model.md)，同步模型的选型见 [ADR-0006](adr/0006-message-sync-model.md)，消息链路的设计与取舍见 [message-pipeline.md](../design/message-pipeline.md)。
>
> 文件上传与下载见第 8 节，音视频通话信令见第 9 节，链路追踪（Trace 上下文如何跨 WebSocket、gRPC 和 Kafka 传递）见第 10 节。

## 1. 服务与通道

```
                 HTTP（登录、资料、好友、群、会话、历史、补拉、已读、上传文件、下载地址）
客户端 ──────────────────────────────────────────→ account / social / message / media
   │
   ├── GET 下载地址（签名即授权）──→ media（本地存储）或阿里云 OSS
   │
   └── WebSocket（认证、心跳、发消息、接收推送、通话信令）──→ access ──gRPC SendMessage──→ message ──gRPC GetFiles──→ media
                                                    ↑                              │ 事务内写 Outbox
                                                    │ gRPC Push / Kick             ↓
                                                 delivery ←── Kafka ←── Outbox 发布器（account、social、message）
```

通话信令另有两条调用（第 9 节）：access 在呼叫前调 social 的 `CheckSendPermission`；access 之间按在线路由直连，用 `Push` 转发来电、接听、SDP 与 ICE。

- 容器模式下客户端只经网关（APISIX）访问：HTTP 接口按路径转发到 account、social、message、media，`/ws` 转发到 access，上游节点经 Consul 发现；网关负责 TLS 和按 IP 的入口限流（超限返回 429，响应体为 `{"error_msg": ...}`，不是 Kratos 错误格式）。宿主机运行时客户端直连各服务端口。路由与端口见 [deployment.md](../deployment.md#网关)。
- **发送只走 WebSocket，读取只走 HTTP**：只有一条写入路径，限流、排除发起连接、按连接顺序确认都只需实现一次。
- 服务间同步调用用 gRPC，经 Consul 发现；调用 access 例外（delivery 推送、access 之间转发通话信令），按在线路由中的地址直连持有连接的实例。
- 服务间异步通知用 Kafka，事件经事务 Outbox 发布，见第 6 节。

## 2. HTTP 请求链路

```
客户端（protobuf JSON，Authorization: Bearer <访问令牌>）
  → Kratos HTTP 服务：访问日志与 panic 恢复 → 令牌校验（验签、声明、吊销名单）→ protovalidate 参数校验
  → service → biz → data（MySQL / Redis / 其他服务的 gRPC）
  → 成功：HTTP 200 + 响应消息；失败：Kratos 错误 {code, reason, message, metadata}，HTTP 状态码即 code
```

中间件链与错误约定见 [system.md](system.md)。不需要登录的接口（注册、登录、发验证码、刷新令牌）由各服务的 `requiresAuth` 跳过令牌校验。

## 3. WebSocket 连接

帧定义见 [api/access/v1/frame.proto](../../api/access/v1/frame.proto)，客户端发 `ClientFrame`，服务端发 `ServerFrame`，每条 WebSocket 消息恰好一帧。

### 3.1 建立连接

1. 客户端连接 `GET ws://<access>:18000/ws`，通过 `Sec-WebSocket-Protocol` 选择编码：
   - `marschat.v1.proto`（不选时的默认值）：二进制消息，protobuf 编码；
   - `marschat.v1.json`：文本消息，protobuf JSON 编码（int64 为字符串、枚举为名字），便于用命令行工具调试。
2. 10 秒内发送第一帧 `Auth{access_token}`。令牌不放 URL：URL 会进入网关和代理的访问日志。
3. access 用与 HTTP 相同的规则校验令牌（`pkg/auth.Verifier`）：
   - 失败：返回 `Error`（`401 UNAUTHORIZED`，或第一帧不是 `Auth` 时 `400 INVALID_FRAME`），然后以 4001 关闭；
   - 成功：分配 63 位随机的连接 ID，回复 `AuthOk{user_id, session_id, connection_id, heartbeat_interval_seconds}`，再登记在线路由。delivery 只能经路由找到连接，所以推送不会早于 `AuthOk`。
4. 浏览器握手时会带 `Origin`，access 不校验：认证靠首帧中的令牌而不是 Cookie，跨站页面拿不到令牌。

### 3.2 心跳与失效

- 客户端每 30 秒发 `Ping`，服务端回 `Pong`，同时续期在线路由（字段 TTL 90 秒）和连接参与的通话（第 9.4 节），并复查吊销名单。
- 90 秒内没有收到任何帧，服务端直接断开（不发关闭帧，客户端看到 1006）。
- 用应用层心跳而不是 WebSocket 控制帧：浏览器不能主动发 ping，客户端也要靠 `Pong` 发现服务端失联。
- 连接**不随访问令牌过期而断开**。失效有两条路径：
  - 立即：退出登录、被踢、同设备重新登录、刷新令牌重放写入 `SessionRevoked` 事件，封禁写入 `UserBanned` 事件；delivery 消费后调用 access 的 `Kick`，客户端收到 `Kicked{reason}` 后连接以 4002 关闭；
  - 兜底：心跳时发现令牌已被吊销，同样发 `Kicked`（原因 `REVOKE_REASON_UNSPECIFIED`）后以 4002 关闭。事件链路故障时，最迟一个心跳周期后断开。Redis 故障导致无法复查时放行，与 HTTP 接口一致。

### 3.3 保护措施

| 项 | 做法 |
|---|---|
| 上行帧大小 | 上限 64 KiB，超出时 gorilla/websocket 以 1009 关闭 |
| 上行帧频率 | 每个连接每秒最多 30 帧（`policy.frames_per_second`，热更新），超出以 4008 关闭。只保护 access 自身，按用户的发消息频率由 message 限制 |
| 发送队列 | 每个连接一个容量 256 的队列，由单独的写协程发送，单帧写超时 10 秒；队列满时以 4003 断开慢连接，客户端重连后补拉 |
| 无法解码的帧、空帧、重复 `Auth` | 认证后返回 `400 INVALID_FRAME`，连接保持 |
| 关闭顺序 | 服务端主动关闭时，先发完队列中的帧和关闭帧，等对端回应关闭帧（最多 2 秒）再断开 TCP，避免对端未读数据触发 RST 导致 `Kicked`、`Error` 帧丢失 |

### 3.4 关闭码

| 关闭码 | 含义 | 客户端处理 |
|---|---|---|
| 4001 | 未认证：第一帧不是 `Auth`、令牌无效、10 秒内未认证 | 刷新令牌后重连 |
| 4002 | 被踢：退出登录、在别处被踢、封禁、令牌被吊销，关闭前发送 `Kicked` | 不自动重连，回到登录页 |
| 4003 | 连接太慢，发送队列已满 | 重连并补拉 |
| 4008 | 上行帧频率超限 | 检查客户端逻辑后重连 |
| 1009 | 上行帧超过 64 KiB | 同上 |
| 1001 | 服务停机（原因 `server shutting down`），停机的实例在 5 秒内分批关闭连接 | 随机等待 0～2 秒后重连（会被分配到其他实例），再补拉，见第 7 节 |

## 4. 发送消息

```
客户端 ─SendMessage{request_id, OutgoingMessage}─→ access（读协程同步处理，同一连接按顺序确认）
  access ─gRPC MessageInternalService.SendMessage{sender_id, session_id, connection_id, message}（超时 3 秒）─→ message
    message：每人每 10 秒 30 条限流（Redis，故障时放行）
           → 查找或创建会话（单聊按两人 ID 唯一，群聊会话 ID 即群 ID）
           → 已存在同一 client_msg_id 时直接返回首次的结果
           → social CheckSendPermission（好友、拉黑、群成员、禁言、解散）
           → 图片、文件消息：media GetFiles 核实文件，用 media 的记录覆盖名称、大小、类型、尺寸（见第 8.3 节）
           → 事务：锁会话行分配 seq → 写消息 → 推进发送者已读 → 写 Outbox MessageCreated
客户端 ←─SendAck{request_id, conversation_id, seq, created_at}── access ←─┘
```

- `client_msg_id` 由客户端为每条消息生成（建议 UUID），超时未确认时用同一个值重发，服务端只存一条，重发返回首次的 seq。
- 失败时返回带同一 `request_id` 的 `Error`：
  - message 的业务错误原样返回：`429 RATE_LIMITED`（metadata 带 `retry_after_seconds`）、`400 VALIDATOR`、social 的拒绝原因（`NOT_FRIENDS`、`BLOCKED`、`NOT_GROUP_MEMBER`、`MEMBER_MUTED`、`GROUP_MUTED`、`GROUP_DISMISSED`）、`400 FILE_INVALID`（图片、文件消息引用的文件不存在、访问密钥不匹配、不是聊天文件，或图片消息引用的不是图片）、`503 DEPENDENCY_UNAVAILABLE`（social 或 media 不可用，无法校验）；
  - 调用 message 失败或超时：`503 UPSTREAM_UNAVAILABLE`。结果未知，消息可能已经写入，客户端用同一个 `client_msg_id` 重发即可；
  - access 到 message 的熔断打开：`503 CIRCUITBREAKER`。请求没有发出，消息确定没有写入，客户端稍后用同一个 `client_msg_id` 重发；
  - 单聊的第一条消息要为新会话生成 ID。Redis 停机超过节点号租约（30 秒）后发号失败，返回 `503 DEPENDENCY_UNAVAILABLE`，消息没有写入，客户端稍后用同一个 `client_msg_id` 重发；已有会话的发送不受影响。
- access 调 message 时，连接级失败（实例在处理途中退出、连接被拒绝）会自动换实例重试一次，靠 `client_msg_id` 幂等不会存两条；message 调 social 同样重试一次。重试与熔断的规则见 [system.md](system.md#53-服务注册与发现)。
- 会话内消息有序靠 seq：同一会话的 seq 在锁住会话行的事务内分配，连续、无空洞。

## 5. 推送、多端同步与补拉

### 5.1 推送

```
message 发布器 ─→ marschat.message.events（键 = 会话 ID，同一会话的事件进同一分区）
delivery（消费组 marschat-delivery）
  → SET NX delivery:done:{event_id} EX 3600，已存在则跳过（先占位后推送：崩溃时宁可漏推也不重复）
  → 确定接收者：单聊取事件中的两人；群聊调 social ListAllGroupMembers（走成员列表缓存）
  → 一次 pipeline HGETALL access:route:{user_id} 取全部在线连接，按 access 实例地址分组
  → 每个实例并发调用一次 Push(connection_ids, ServerFrame)，每批最多 1000 个连接
access ─→ 把帧放进各连接的发送队列 ─→ 客户端收到 Message 或 Read 帧（request_id 为 0）
```

| 事件 | 接收连接 | 推送帧 |
|---|---|---|
| `MessageCreated` | 会话成员的全部在线连接，**排除发起发送的连接**（它已收到 `SendAck`）。发送者的其他设备、其他标签页也能收到 | `Message` |
| `ReadPositionUpdated` | 本人的全部连接（多端同步未读数）；单聊再加上对方（已读回执）。不排除发起端，客户端收到不大于本地的值时忽略 | `Read` |
| `SessionRevoked` | 该登录会话的全部连接 | 调用 `Kick`，原因取事件中的原因 |
| `UserBanned` | 该用户的全部连接 | 调用 `Kick`，原因 `BANNED` |
| social 的好友与群事件 | 见 5.4 节 | `Notification` |

- 推送尽力而为：查路由、调用 access 失败都只记日志，不重试，不阻塞后面的事件。可靠性由补拉保证。
- access 实例崩溃后，它的路由最多残留 90 秒（字段 TTL）。delivery 第一次推送失败（`Unavailable` 或超时）后摘除到该实例的连接，地址冷却 5 秒（`policy.push_cooldown`），冷却期内发往它的推送直接跳过，不再每条事件都等一次超时；冷却结束后再试一次，仍然失败就再冷却。
- delivery 的消费组新建时从最新位置开始：推送只对在线连接有意义，历史消息由客户端补拉。

### 5.2 客户端同步流程

客户端按会话维护本地的 `max_seq`（收到的最大 seq）和已读位置：

1. 连接认证成功后，`GET /v1/conversations` 取全部会话（每个会话带 `max_seq`、`read_seq`、`unread_count`、`peer_read_seq`、最后一条消息）。
2. 服务端 `max_seq` 大于本地的会话，调 `GET /v1/conversations/{id}/difference?from_seq=<本地 max_seq>` 补拉：
   - `COMPLETE`：已取完；
   - `SLICE`：还有更多，用 `next_from_seq` 继续；
   - `TOO_LONG`：缺口超过 1000 条（`policy.difference_too_long`），放弃补拉，改用 `GET .../messages` 加载最新一页。
3. 之后靠推送增量更新。收到推送的 seq 不等于本地 `max_seq + 1` 时（推送丢失或乱序），按第 2 步补拉缺口。
4. 收到不认识的会话的消息时，调 `GET /v1/conversations/{id}` 取会话信息。
5. 阅读后 `POST /v1/conversations/{id}/read {seq}`，已读位置只增不减，超过 `max_seq` 时按 `max_seq` 处理；变化后其他设备和单聊对方会收到 `Read` 推送。

### 5.3 群成员与会话

群会话的成员由 message 消费 social 的群事件维护（消费组 `marschat-message`），发消息时不会创建成员：

| social 事件 | message 的处理 |
|---|---|
| `GroupCreated` | 初始成员加入会话，已读位置 0 |
| `GroupMembersJoined` | 新成员加入，已读位置取当前 `max_seq`：能看历史，但历史不算未读 |
| `GroupMemberLeft` | 删除成员，之后看不到会话 |
| `GroupDismissed` | 不处理：会话保留，成员仍能看历史；social 拒绝之后的发言 |

成员投影按分区顺序生效，处理失败时原地重试，不跳过，也不使用重试队列；内容有问题的事件（`Permanent`）写入死信队列。发言权限以 social 的实时校验为准，不依赖这份投影。

### 5.4 好友与群通知

delivery 同一个消费组 `marschat-delivery` 也订阅 `marschat.social.events`（步骤 5 新增，从订阅时的最新位置开始），把事件原样包进 `Notification` 帧推给相关用户的全部在线连接：

```
social 发布器 ─→ marschat.social.events ─→ delivery
  → SET NX delivery:done:{event_id}（与消息推送共用去重）
  → 确定接收用户（下表），群事件经 social ListAllGroupMembers 取当前成员
  → 查在线路由，按 access 实例分组调用 Push ─→ 客户端收到 ServerFrame.notification
```

| 事件（`Notification.body`） | 接收用户 |
|---|---|
| `friend_request_created` | 被申请人 `to_user_id` |
| `friend_added`、`friend_deleted` | 双方 |
| `group_created` | 事件中的 `member_ids`（含群主） |
| `group_members_joined` | 群的当前全部成员，并上事件中的新成员 |
| `group_member_left` | 群的当前全部成员，加上离开的人 |
| `group_member_updated` | 群的当前全部成员，并上被修改的成员 |
| `group_updated` | 群的当前全部成员 |
| `group_dismissed` | 事件中解散前的 `member_ids`（不再查询成员） |
| `group_join_request_created` | 群主和管理员 |

- 推送尽力而为，与消息推送相同：查不到群成员、查路由或调用 access 失败都只记日志，这条通知不再推送。断线期间的通知不补发，客户端重连后通过列表接口刷新好友、申请和群。
- 事件不带发起连接，操作者自己的连接也会收到。并上事件中的当事人，是为了在成员列表缓存还没刷新时，新成员、被修改的成员也能收到。
- `Notification.event_id` 全局唯一，客户端可以用它去重；也可以直接按通知类型刷新对应的数据。

## 6. 事件与 Outbox

| topic | 分区 | 生产方 | 事件 | 消费方（消费组） |
|---|---|---|---|---|
| `marschat.account.events` | 3 | account | `SessionRevoked`、`UserBanned`（键为用户 ID） | delivery（`marschat-delivery`）：踢连接 |
| `marschat.social.events` | 6 | social | 好友、群、申请事件，见 [events.proto](../../api/social/v1/events.proto) | message（`marschat-message`）：群成员投影；social（`marschat-social-cache`）：兜底删除群缓存；delivery（`marschat-delivery`）：通知 |
| `marschat.message.events` | 12 | message | `MessageCreated`、`ReadPositionUpdated`（键为会话 ID） | delivery（`marschat-delivery`）：推送 |
| `marschat.social-cache.retry` | 3 | social（`marschat-social-cache` 转入） | 删除缓存失败的群事件，原样转入并带最早处理时间 | social（`marschat-social-cache-retry`）：10 秒后再删 |
| `marschat.dlq` | 3 | 所有消费者 | 处理不了的记录（`Permanent`、解码失败、panic），保留 14 天 | 无，只供排查，查看方法见 [deployment.md](../deployment.md#死信队列) |

- **写入**：业务事务内插入 `outbox_events`，与业务数据同时提交或回滚，不在事务里调用 Kafka。
- **发布**：每个服务同时只有一个实例发布（Redis 租约 `outbox:lease:{服务名}`），保证同一个键的事件按写入顺序进入 Kafka。事务提交后立即唤醒发布器：本实例就是持有者时直接唤醒，否则经 Redis 频道 `outbox:wake:{服务名}` 唤醒持有者；轮询（500 毫秒）只是兜底。Kafka 不可用时退避重试（1 秒起，最长 30 秒），事件留在表中只会延迟、不会丢；发布后、标记前崩溃会重复发布。
- **记录格式**：键为 `event_key`，值为事件的 protobuf 编码，记录头 `event_type`（proto 全名）、`event_id`（`{服务名}:{outbox id}`）、`source`，事件写入时有 Trace 上下文的还带 `traceparent`（见第 10 节）。
- **消费**：关闭自动提交，按分区顺序处理后只提交已处理的位点。未注册的事件类型跳过。失败分两类：
  - **处理不了**（处理器返回 `Permanent`、解码失败、panic）：记 ERROR，把原始的键、值和头写入 `marschat.dlq`，追加头 `x-dlq-group`、`x-dlq-topic`、`x-dlq-partition`、`x-dlq-offset`、`x-dlq-error`，然后提交位点。写入失败时不提交，退避重试写入，记录不会凭空消失。
  - **临时失败**（其他错误）：退避重试（100 毫秒起，最长 5 秒）。不要求顺序的消费者（目前只有 `marschat-social-cache`）原地尝试 3 次仍失败，就把记录转入自己的重试 topic（头 `x-retry-not-before` 为 10 秒后、`x-retry-from` 为原位置）并提交原位点，主分区不被阻塞；重试消费者（消费组加 `-retry` 后缀，同样的处理器）等到时间后处理，临时失败原地无限重试，只阻塞重试 topic。要求顺序的消费者（`marschat-message`）原地无限重试，连续失败 10 次后改记 ERROR（每条记录每分钟最多一次）。
  - delivery 的处理器从不返回错误，只有解码失败会进死信队列。
- **幂等**：message 的成员投影靠 SQL 本身幂等；delivery 用 `delivery:done:{event_id}` 去重；踢连接、删除缓存本身幂等。

## 7. 停机

收到 SIGINT/SIGTERM 后，Kratos 先从 Consul 注销，再并发停止各个 server：

- access：
  1. 从 Consul 注销后，网关的 Consul 长轮询约 1 秒内更新上游，新的 WebSocket 握手不再分到这个实例；HTTP 服务器同时关闭监听，已经发往它的握手连接失败，由网关换实例重试。
  2. Hub 拒绝新连接，把现有连接以 1001 分批关闭，均匀分布在 `websocket.drain_window`（5 秒）内，避免所有客户端同时重连到剩下的实例。每条连接等待对端回应关闭帧（最多 2 秒），关闭时删除路由，并结束它参与的通话（第 9.4 节）。
  3. gRPC 服务器等 Hub 排空后才停止，所以排空期间 delivery 发往尚未关闭的连接的推送仍然送达。
  4. 停机超时 15 秒，到期后强制断开剩余连接。
- 客户端约定：
  - 收到 1001 或连接异常断开（1006）后，随机等待 0～2 秒再重连，避免同时重连；
  - 重连认证成功后，按第 5.2 节对每个会话从本地 `max_seq` 补拉；
  - 补拉与推送可能重叠，按 `(conversation_id, seq)` 去重。
  - 按这套约定实现的测试客户端在杀掉 access、滚动重启 access 的故障实验中没有丢失和重复，见[重构计划的故障实验结果](../plans/completed/backend-refactor.md#故障实验结果)。
- Outbox 发布器：发完当前批次后释放租约，其他实例下一次抢占时接手。
- Kafka 消费者：处理完当前记录、提交位点后离开消费组。

## 8. 文件上传与下载

设计见 [media-service.md](../design/media-service.md)，存储选型见 [ADR-0007](adr/0007-object-storage.md)。

### 8.1 上传

```
客户端 ─POST /v1/files?purpose=PURPOSE_CHAT&name=<文件名>（Authorization，请求体为文件原始字节，带 Content-Length）─→ media
  media：令牌校验 → 参数校验 → 每人每小时 60 次限流（Redis，故障时放行）
       → 检查 Content-Length（缺失 411、为 0 返回 400、超过用途上限 413，都不读请求体）
       → 读前 512 字节按内容识别类型（头像不是 JPEG/PNG/GIF/WebP 时 415）
       → 不超过 10 MiB 的光栅图片：整体读入，解码（不超过 1600 万像素）并生成 320 像素的 JPEG 缩略图；解码失败按普通文件
         其他文件：边读边写入存储，同时计算 SHA-256
       → 写入存储（local：临时文件 + rename；oss：PutObject）→ 插入 files 记录（失败时删除刚写入的对象）
客户端 ←─FileInfo{id, accessKey, purpose, kind, name, size, mimeType, width, height, hasThumbnail}──┘
```

- 请求体没读完（客户端中断、超过 300 秒的服务器超时）返回 `400 UPLOAD_INCOMPLETE`，不留下记录；存储不可用、图片处理排队超过 5 秒返回 `503 DEPENDENCY_UNAVAILABLE`。
- 容器模式下网关对 `/v1/files` 限制请求体 101 MiB，超过时网关直接返回 413，不转发。

### 8.2 下载

```
客户端 ─POST /v1/files/download-urls{items: [{fileId, accessKey, variant}]}（Authorization，最多 100 项）─→ media
  media：批量读元数据（缓存）→ 逐项校验：头像所有登录用户可读；聊天文件需要访问密钥匹配或是上传者；
         缩略图只有图片才有；写在切换前另一种存储中的文件跳过 → 本地计算签名地址（有效期 10 分钟）
客户端 ←─urls: [{fileId, variant, url, expiresAt}]（校验不通过的项省略，不区分原因）──┘

客户端 ─GET <url>（不需要访问令牌，可以直接用于 <img src>）─→
  local：media GET /v1/files/{id}/content?variant=&expires=&signature= 校验 HMAC 签名与过期时间后返回文件（支持 Range）
  oss：  阿里云 OSS 校验 V4 预签名后返回对象
```

- 光栅图片与缩略图以 `inline` 和识别出的类型返回；其他文件（包括 SVG、HTML）一律 `attachment` 加 `application/octet-stream`，local 另加 `X-Content-Type-Options: nosniff`。OSS 的响应头由预签名中的 `response-content-type`、`response-content-disposition` 决定，它们参与签名，客户端改不了。
- local 的签名错误或过期返回 `403 DOWNLOAD_URL_INVALID`，客户端重新换取地址。

### 8.3 图片与文件消息

客户端上传后，把 `FileInfo` 中的 `id` 和 `accessKey` 放进 `ImageContent` / `FileContent`，经 WebSocket 发送（第 4 节）。message 调 media 的 `GetFiles` 核实，用 media 的记录覆盖名称、大小、类型和尺寸后落库。接收方从推送或历史中拿到文件 ID 和访问密钥，按 8.2 换取缩略图与原文件的地址。转发时访问密钥随消息一起转发，不需要额外授权。

## 9. 通话信令

设计见 [call-signaling.md](../design/call-signaling.md)，帧定义见 [api/access/v1/call.proto](../../api/access/v1/call.proto)。一对一音视频通话的媒体走 WebRTC 点对点，服务端只协调状态、转发 SDP 与 ICE；信令不落库、不经 Kafka。

### 9.1 流程

主叫用户 A 的连接 `a` 呼叫用户 B，B 有两个连接 `b1`、`b2`，可以在不同的 access 实例上：

1. `a` 发 `CallRequest{invite}`。`a` 所在的 access：
   - 调 social `CheckSendPermission`（单聊规则：好友且双方都没有拉黑）；
   - 查 B 的在线路由，没有路由时返回 `404 CALL_PEER_OFFLINE`；
   - 在 Redis 中用 Lua 创建通话（状态 `ringing`），同时占用 A、B 的忙线标记，任一方已在通话中时分别返回 `409 ALREADY_IN_CALL`、`409 CALL_BUSY`；
   - 先回复 `a`：`CallAck{call_id}`，再把 `CallEvent{incoming}` 推给 `b1`、`b2`，并启动振铃定时器（`call.ring_timeout`，60 秒）。
2. `b1` 发 `CallRequest{accept}`。`b1` 所在的 access：
   - 在 Redis 中把状态改为 `active`，记下接听连接 `b1`；
   - 先回复 `b1`：`CallAck`，再向 `b2` 推送 `CallEvent{ended: ANSWERED_ELSEWHERE}`，最后向 `a` 推送 `CallEvent{accepted}`。
3. `a` 收到 `accepted` 后创建 offer，用 `CallRequest{signal}` 发送；access 从 Redis 中找到接听连接，把同一个 `CallSignal` 作为 `CallEvent{signal}` 推给 `b1`。answer 和双方的 ICE 候选按同样的方式转发。SDP 与 ICE 只在 `a`、`b1` 之间转发，不回显给发送方，接听前发送返回 `404 CALL_NOT_FOUND`。
4. 任何一方发 `CallRequest{hangup}`：删除通话与双方的忙线标记，对方收到 `CallEvent{ended: HANGUP}`。

推送给其他实例上的连接时，access 按在线路由中的地址直连对端实例，调用它的 `AccessInternalService.Push`（与 delivery 共用 `pkg/accessclient`：实例不可达时摘除连接，冷却 `call.forward_cooldown` 5 秒）；本实例的连接直接放进发送队列。同一连接发出的信令按发送顺序到达对端。

### 9.2 各操作的条件与通知

| 操作 | 谁能发起 | 收到 `CallEnded` 等通知的连接 |
|---|---|---|
| 邀请 | 任意连接 | 被叫全部连接：`incoming` |
| 接听 | 被叫的任一连接，振铃中 | 主叫连接：`accepted`；被叫其他连接：`ended{ANSWERED_ELSEWHERE}` |
| 拒绝 | 被叫的任一连接，振铃中 | 主叫连接与被叫其他连接：`ended{REJECTED}` |
| 挂断 | 主叫连接（振铃中即取消），或接听连接 | 振铃中：被叫全部连接；通话中：对方连接。`ended{HANGUP}` |
| 振铃超时 | 主叫所在 access 的定时器 | 主叫连接与被叫全部连接：`ended{TIMEOUT}` |
| 断线 | 主叫连接或接听连接关闭 | 振铃中：被叫全部连接；通话中：对方连接。`ended{DISCONNECTED}` |

振铃中的被叫只能拒绝，不能挂断；被叫没有接听的设备断开不影响通话。

### 9.3 回复与失败

- 每个 `CallRequest` 都收到带同一 `request_id` 的 `CallAck` 或 `Error`。`Error` 表示请求被拒绝、状态没有变化：

  | 错误 | 场景 |
  |---|---|
  | `400 INVALID_FRAME` | 字段不合法（protovalidate）、呼叫自己 |
  | social 的 `403 NOT_FRIENDS`、`403 BLOCKED`、`404 USER_NOT_FOUND` | 呼叫的权限，原样来自 social |
  | `404 CALL_PEER_OFFLINE` | 被叫没有在线连接 |
  | `404 CALL_NOT_FOUND` | 通话不存在或已结束、当前连接不是通话方、状态不允许该操作 |
  | `409 CALL_BUSY` / `409 ALREADY_IN_CALL` | 被叫 / 自己（包括其他设备）已在通话或振铃中 |
  | `409 CALL_ALREADY_ANSWERED` | 已被被叫的另一台设备接听后，再接听或拒绝 |
  | `503 UPSTREAM_UNAVAILABLE` | social 或 Redis 不可用。不降级：没有共享状态就无法保证互斥接听 |

- `CallAck` 表示请求已被接受，之后通话如果结束，一律经 `CallEnded` 通知，包括刚收到 `CallAck` 的连接自己：
  - 邀请时路由还在、但被叫的连接其实都已失效（推送全部失败），主叫收到 `ended{PEER_OFFLINE}`；
  - 转发给对方连接（`accepted`、`signal`）失败时按断线结束，发起请求的连接与对方都收到 `ended{DISCONNECTED}`。

### 9.4 超时与清理

通话状态在 Redis 中（`access:call:{call_id}` 与 `access:call_user:{uid}`，见 [data-model.md](data-model.md)），所有 access 实例共享：

- **振铃超时**：主叫所在 access 的进程内定时器到期时，通话仍在振铃就结束。接听、拒绝、取消先发生时什么也不做。
- **断线**：连接关闭（心跳超时、被踢、停机排空、客户端断开）时，如果它是主叫连接或接听连接，就按断线结束通话。access 滚动重启也会因此结束正在进行的通话。
- **续期**：通话中状态的 TTL 为心跳超时（90 秒），主叫连接与接听连接每次心跳续期；振铃中的 TTL 为振铃超时加 15 秒。
- **access 崩溃**：没有机会清理，状态按 TTL 过期。主叫所在实例崩溃时没有人发出 `CallEnded`；接听方所在实例崩溃时，主叫下一次发信令失败，通话按断线结束（集群故障实验见[重构计划](../plans/completed/backend-refactor.md#故障实验结果)）。

客户端约定：

- 被叫收到来电后最多响铃 `ring_timeout` + 5 秒（65 秒），之后自行停止；
- ICE 候选可能早于 SDP 到达，暂存到设置远端描述之后再添加；
- WebRTC 连接失败（`iceconnectionstatechange` 为 `failed`）时发送挂断；
- 不认识的 `call_id` 的事件直接忽略。

### 9.5 尚未实现

群通话；STUN/TURN 服务器与临时凭据下发（ICE 服务器为空时只能在可以直接互通的网络中建立连接，随前端重构部署 coturn）；通话记录写入聊天记录；断线重连后恢复通话。

## 10. 链路追踪

一条消息从发送到推送是一条 Trace（容器模式下在 Jaeger 中查看；没有配置 `telemetry.otlp_endpoint` 时不导出）：

```
access    ws.SendMessage              服务端 Span：user_id、conn_id、request_id、client_msg_id、conversation_id、seq
└ access → message  SendMessage       gRPC 客户端 Span
  └ message  SendMessage              gRPC 服务端 Span
    ├ message → social  CheckSendPermission
    ├ message → media  GetFiles       只有图片、文件消息
    └ message  publish marschat.message.events    生产者 Span，起点为事件写入 Outbox 的时刻
      └ delivery  consume marschat.message.events  消费者 Span：conversation_id、seq、queue_ms
        └ delivery → access  Push     每个在线的 access 实例一个客户端 Span
          └ access  Push              gRPC 服务端 Span
```

- **传递方式**：
  - HTTP、gRPC：Kratos tracing 中间件按 W3C Trace Context 传递（HTTP 头、gRPC 元数据中的 `traceparent`）。
  - WebSocket 上行帧：帧里不带 Trace 上下文，access 为每个 `SendMessage`、`Call` 帧开启新的根 Span。
  - Outbox 与 Kafka：`outbox.Add` 在业务事务中把当前 Span 的上下文写入 `outbox_events.trace_parent`（没有 Span 时为空字符串）；发布器读出后开启生产者 Span，把它的上下文写入 Kafka 记录头 `traceparent`；消费者读出记录头，开启消费者 Span，处理器在它的 context 中执行。转入重试队列、死信队列的记录复制原记录的头，重试消费者的 Span 仍在同一条 Trace 下。
- **时间的含义**：生产者 Span 从事件写入时刻开始、到 Kafka 确认结束，长度就是“在 Outbox 中等待 + 发送到 Kafka”；消费者 Span 的 `marschat.queue_ms` 是记录在 Kafka 中等待的时间；消费者 Span 覆盖全部原地重试，每次失败作为 Span 事件记录。
- **采样**：在 Trace 的根（WebSocket 帧，或者第一个收到请求的 HTTP、gRPC 服务端）决定，下游跟随上游，一条 Trace 要么完整、要么不导出。
- 同一批发布的多条事件各有自己的生产者 Span。一个事件被多个消费组消费时（例如 social 事件同时被 delivery、message、social 自己消费），每个消费组各有一个消费者 Span，挂在同一个生产者 Span 下。
- 日志中的 `trace_id` 与 Jaeger 中的一致，可以从一条错误日志找到整条链路。
