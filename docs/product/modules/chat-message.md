# 会话与消息

> **重构说明（2026-11-19）**：“新实现”一节描述 message、access、delivery 的当前代码；其后各节描述已移除的旧基线（截至提交 `6624cea`），其中的代码位置已不存在，只作功能参考。

## 新实现（重构步骤 4）

设计与取舍见 [message-pipeline.md](../../design/message-pipeline.md)，同步模型见 [ADR-0006](../../architecture/adr/0006-message-sync-model.md)，WebSocket 帧、推送与补拉流程见 [data-flow.md](../../architecture/data-flow.md)，功能状态见 [features.md](../features.md#新后端)，表与 Redis 键见 [data-model.md](../../architecture/data-model.md#23-messagemarschat_message迁移-000001_init)。

| 层 | 位置 |
|---|---|
| 接口 | `api/message/v1/message.proto`、`internal.proto`、`error_reason.proto`、`events.proto`；`api/access/v1/frame.proto`、`internal.proto`、`error_reason.proto` |
| message | `app/message/internal/biz/message.go`（发送）、`conversation.go`（会话、历史、补拉、已读）、`membership.go`（群成员投影）；`internal/server/consumer.go`（消费 social 群事件） |
| access | `app/access/internal/biz/hub.go`、`conn.go`（连接管理）；`internal/server/server.go`（`/ws` 与内部 gRPC） |
| delivery | `app/delivery/internal/biz/delivery.go`；`internal/server/server.go`（消费消息与账号事件） |
| 公共 | `pkg/kafka`、`pkg/outbox`、`pkg/route`、`pkg/auth`（`Verifier`） |

与旧基线的主要差别：

- 读扩散：消息按会话只存一份，会话内 seq 连续递增；每个成员只记已读位置，未读数 = `max_seq - read_seq`（旧基线没有未读数）。
- 发送只走 WebSocket，写库成功才返回 `SendAck`；客户端生成 `client_msg_id`，重发只存一条。发送者自己的消息由客户端本地展示，不再依赖服务端回显（旧基线见 [ADR-0003](../../architecture/adr/0003-server-side-echo.md)），发送者的其他设备经推送同步。
- 连接首帧认证、心跳续期在线路由、慢连接断开；被踢、封禁、退出登录会立即断开连接（修正旧基线握手不校验身份、连接生命周期混乱的问题）。
- 推送经 Kafka 和 delivery 跨实例投递，推送尽力而为，可靠性靠补拉（`GetDifference`）保证；历史消息分页。
- 会话不再需要“打开”：第一条消息时自动创建；群会话的成员随入群、退群事件变化，新成员能看历史但不算未读。删除会话（旧基线的 `deleteSession`）暂未实现。
- 消息类型目前只有文本（`TextContent`，1～4000 字符）。文件、图片等随 media 服务在步骤 6 加入，通话信令在步骤 7 加入。

接口（HTTP 全部需要登录，同名的 gRPC 方法见 proto）：

| 方法 | HTTP / 通道 | 说明 |
|---|---|---|
| `ListConversations` | `GET /v1/conversations` | 按会话 ID 游标分页，`limit` 默认 20、最大 100；每个会话带 `max_seq`、`read_seq`、`unread_count`、单聊对方的已读位置 `peer_read_seq`、最后一条消息、对方资料或群资料（依赖不可用时为空） |
| `GetConversation` | `GET /v1/conversations/{conversation_id}` | 字段同上；不是成员时为 `404 CONVERSATION_NOT_FOUND` |
| `ListMessages` | `GET /v1/conversations/{conversation_id}/messages` | 从 `before_seq` 往前翻页，按 seq 降序，`limit` 默认 20、最大 100 |
| `GetDifference` | `GET /v1/conversations/{conversation_id}/difference` | 补拉 `from_seq` 之后的消息，结果为 `COMPLETE`、`SLICE`（继续用 `next_from_seq`）或 `TOO_LONG`（缺口超过 1000 条） |
| `MarkRead` | `POST /v1/conversations/{conversation_id}/read` | 已读位置只增不减，超过 `max_seq` 时按 `max_seq` |
| 建立连接 | `GET ws://<access>:18000/ws` | 帧与关闭码见 [frame.proto](../../../api/access/v1/frame.proto) 和 [data-flow.md](../../architecture/data-flow.md#3-websocket-连接) |
| 发送消息 | WebSocket `SendMessage` 帧 | 每人每 10 秒 30 条；返回 `SendAck` 或 `Error` |
| `MessageInternalService.SendMessage` | 仅 gRPC（内网） | access 调用，发送者取自已认证的连接 |
| `AccessInternalService.Push` / `Kick` | 仅 gRPC（内网） | delivery 按在线路由直连调用 |

错误原因见 [message](../../../api/message/v1/error_reason.proto)、[access](../../../api/access/v1/error_reason.proto) 的 `error_reason.proto`；发言权限的拒绝原因沿用 social 的错误（`NOT_FRIENDS`、`BLOCKED`、`NOT_GROUP_MEMBER`、`MEMBER_MUTED`、`GROUP_MUTED`、`GROUP_DISMISSED`）。

## 职责

会话列表的创建与删除；单聊、群聊的实时消息与文件消息；历史消息查询。旧基线实时转发的内部机制见提交 `6624cea` 中 `data-flow.md` 的第 3 节。

## 代码位置

| 层 | 位置 |
|---|---|
| 后端 | `api/v1/session_controller.go`、`message_controller.go`、`ws_controller.go`；`internal/service/gorm/session_service.go`、`message_service.go`；`internal/service/chat/` |
| 前端 | `views/chat/session/SessionList.vue`、`views/chat/contact/ContactChat.vue` |

## 会话

- 会话按所有者单向保存（`session.send_id` 是所有者）。A 打开与 B 的聊天时只创建 A 的会话；B 收到消息不会自动生成会话，需要 B 自己从联系人列表进入。
- `/session/openSession` 查找已有会话，没有就创建；创建时若对方已被禁用则失败。
- 会话列表按单聊（`U...`）和群聊（`G...`）分两个接口返回，均带 Redis 缓存。
- 删除会话只软删除自己的记录，不影响消息。
- 会话没有“最后一条消息”和未读数（相关字段未被写入）。

## 消息

| 类型 | 发送方式 | 展示 |
|---|---|---|
| 文本（0） | WebSocket 直接发送 | 气泡 |
| 文件（2） | 先上传到 `/message/uploadFile`，再发送带 `url` 的 WebSocket 消息；图片、视频等都按文件处理 | 文件卡片，可下载；不内嵌预览或播放 |
| 通话信令（3） | WebSocket | 不渲染，见 [av-call.md](av-call.md) |
| 语音（1） | 仅有枚举定义 | — |

- 离线消息：只落库，接收方打开会话时通过历史接口读取，没有离线通知或未读提醒。
- 历史消息一次返回全部记录，无分页。
- 前端只实时显示属于当前打开会话的消息。

## 接口

| 路径 | 请求字段 | 说明 |
|---|---|---|
| `/session/checkOpenSessionAllowed` | `send_id`、`receive_id` | 检查拉黑与禁用状态 |
| `/session/openSession` | `send_id`、`receive_id` | 打开或创建会话，返回会话 ID |
| `/session/getUserSessionList` | `owner_id` | 单聊会话列表 |
| `/session/getGroupSessionList` | `owner_id` | 群聊会话列表 |
| `/session/deleteSession` | `owner_id`、`session_id` | 删除会话 |
| `/message/getMessageList` | `user_one_id`、`user_two_id` | 单聊历史 |
| `/message/getGroupMessageList` | `group_id` | 群聊历史 |
| `/message/uploadFile` | multipart 文件 | 上传聊天文件 |
| `GET /wss?client_id=<uuid>` | — | 建立 WebSocket，消息格式见提交 `6624cea` 中 `data-flow.md` 的 3.1 节 |
| `/chatroom/getCurContactListInChatRoom` | `owner_id`、`contact_id` | 恒返回空列表，前端未使用（TD-20） |

## 相关问题

TD-04（异常消息导致崩溃）、TD-08（连接生命周期）、TD-09（消息缓存无效）、TD-10（文件上传）、TD-14（重复代码）、TD-22（消息状态语义、无分页）。
