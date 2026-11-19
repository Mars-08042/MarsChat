# 数据流

> 描述请求、消息和事件在服务之间如何流转，WebSocket 帧协议以本文为准。随重构步骤更新，最近一次更新于步骤 4（2026-11-19）。服务内分层见 [system.md](system.md)，表与缓存键见 [data-model.md](data-model.md)，同步模型的选型见 [ADR-0006](adr/0006-message-sync-model.md)，步骤 4 的设计与取舍见 [message-pipeline.md](../design/message-pipeline.md)。
>
> 文件上传（步骤 6）和音视频通话信令（步骤 7）尚未重写，旧基线的流程见提交 `6624cea` 中本文的第 5、6 节。

## 1. 服务与通道

```
                 HTTP（登录、资料、好友、群、会话、历史、补拉、已读）
客户端 ──────────────────────────────────────────→ account / social / message
   │
   └── WebSocket（认证、心跳、发消息、接收推送）──→ access ──gRPC SendMessage──→ message
                                                    ↑                              │ 事务内写 Outbox
                                                    │ gRPC Push / Kick             ↓
                                                 delivery ←── Kafka ←── Outbox 发布器（account、social、message）
```

- 网关（APISIX）在步骤 5 接入，在那之前客户端直连各服务端口，端口见 [deployment.md](../deployment.md)。
- **发送只走 WebSocket，读取只走 HTTP**：只有一条写入路径，限流、排除发起连接、按连接顺序确认都只需实现一次。
- 服务间同步调用用 gRPC，经 Consul 发现；delivery 调用 access 例外，按在线路由中的地址直连持有连接的实例。
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

- 客户端每 30 秒发 `Ping`，服务端回 `Pong`，同时续期在线路由（字段 TTL 90 秒）并复查吊销名单。
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
| 1001 | 服务停机 | 重连（会被分配到其他实例）并补拉 |

## 4. 发送消息

```
客户端 ─SendMessage{request_id, OutgoingMessage}─→ access（读协程同步处理，同一连接按顺序确认）
  access ─gRPC MessageInternalService.SendMessage{sender_id, session_id, connection_id, message}（超时 3 秒）─→ message
    message：每人每 10 秒 30 条限流（Redis，故障时放行）
           → 查找或创建会话（单聊按两人 ID 唯一，群聊会话 ID 即群 ID）
           → 已存在同一 client_msg_id 时直接返回首次的结果
           → social CheckSendPermission（好友、拉黑、群成员、禁言、解散）
           → 事务：锁会话行分配 seq → 写消息 → 推进发送者已读 → 写 Outbox MessageCreated
客户端 ←─SendAck{request_id, conversation_id, seq, created_at}── access ←─┘
```

- `client_msg_id` 由客户端为每条消息生成（建议 UUID），超时未确认时用同一个值重发，服务端只存一条，重发返回首次的 seq。
- 失败时返回带同一 `request_id` 的 `Error`：
  - message 的业务错误原样返回：`429 RATE_LIMITED`（metadata 带 `retry_after_seconds`）、`400 VALIDATOR`、social 的拒绝原因（`NOT_FRIENDS`、`BLOCKED`、`NOT_GROUP_MEMBER`、`MEMBER_MUTED`、`GROUP_MUTED`、`GROUP_DISMISSED`）、`503 DEPENDENCY_UNAVAILABLE`（social 不可用，无法校验权限）；
  - 调用 message 失败或超时：`503 UPSTREAM_UNAVAILABLE`。消息可能已经写入，客户端用同一个 `client_msg_id` 重发即可。
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

- 推送尽力而为：查路由、调用 access 失败都只记日志，不重试，不阻塞后面的事件。可靠性由补拉保证。
- access 实例崩溃后，它的路由最多残留 90 秒（字段 TTL），期间推送到这些连接的调用失败，只记日志。
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

成员投影按分区顺序生效，处理失败时原地重试，不跳过。发言权限以 social 的实时校验为准，不依赖这份投影。

## 6. 事件与 Outbox

| topic | 分区 | 生产方 | 事件 | 消费方（消费组） |
|---|---|---|---|---|
| `marschat.account.events` | 3 | account | `SessionRevoked`、`UserBanned`（键为用户 ID） | delivery（`marschat-delivery`）：踢连接 |
| `marschat.social.events` | 6 | social | 好友、群、申请事件，见 [events.proto](../../api/social/v1/events.proto) | message（`marschat-message`）：群成员投影；social（`marschat-social-cache`）：兜底删除群缓存 |
| `marschat.message.events` | 12 | message | `MessageCreated`、`ReadPositionUpdated`（键为会话 ID） | delivery（`marschat-delivery`）：推送 |

- **写入**：业务事务内插入 `outbox_events`，与业务数据同时提交或回滚，不在事务里调用 Kafka。
- **发布**：每个服务同时只有一个实例发布（Redis 租约 `outbox:lease:{服务名}`），保证同一个键的事件按写入顺序进入 Kafka。事务提交后立即唤醒发布器，轮询（500 毫秒）只是兜底。Kafka 不可用时退避重试（1 秒起，最长 30 秒），事件留在表中只会延迟、不会丢；发布后、标记前崩溃会重复发布。
- **记录格式**：键为 `event_key`，值为事件的 protobuf 编码，记录头 `event_type`（proto 全名）、`event_id`（`{服务名}:{outbox id}`）、`source`。
- **消费**：关闭自动提交，按分区顺序处理后只提交已处理的位点。未注册的事件类型跳过；解码失败记 ERROR 后跳过；处理器返回错误时原地退避重试（100 毫秒起，最长 5 秒）。delivery 的处理器从不返回错误。重试队列与死信队列在步骤 5 实现，在那之前持续失败的事件会阻塞所在分区（TD-30）。
- **幂等**：message 的成员投影靠 SQL 本身幂等；delivery 用 `delivery:done:{event_id}` 去重；踢连接、删除缓存本身幂等。

## 7. 停机

收到 SIGINT/SIGTERM 后，Kratos 先从 Consul 注销，再并发停止各个 server：

- access：拒绝新连接，以 1001 关闭全部连接并删除它们的路由，最多等待到停机超时，超时后强制断开。先从 Consul 摘除、通知客户端分批重连在步骤 5 实现。
- Outbox 发布器：发完当前批次后释放租约，其他实例下一次抢占时接手。
- Kafka 消费者：处理完当前记录、提交位点后离开消费组。
