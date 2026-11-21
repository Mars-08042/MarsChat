# 会话与消息

> **重构说明（2026-11-19）**：两个“新实现”小节描述 message、access、delivery 和 media（步骤 6）的当前代码；其后各节描述已移除的旧基线（截至提交 `6624cea`），其中的代码位置已不存在，只作功能参考。

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
- 消息类型有文本（`TextContent`，1～4000 字符）、图片（`ImageContent`）和文件（`FileContent`），图片与文件见下一节；通话信令不是消息，见 [av-call.md](av-call.md#新实现信令重构步骤-7)。

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

错误原因见 [message](../../../api/message/v1/error_reason.proto)、[access](../../../api/access/v1/error_reason.proto) 的 `error_reason.proto`；发言权限的拒绝原因沿用 social 的错误（`NOT_FRIENDS`、`BLOCKED`、`NOT_GROUP_MEMBER`、`MEMBER_MUTED`、`GROUP_MUTED`、`GROUP_DISMISSED`）；图片、文件消息另有 `FILE_INVALID`，见下一节。

## 新实现：文件与图片（重构步骤 6）

media 服务提供上传、下载地址和给 message 用的内部文件查询。设计见 [media-service.md](../../design/media-service.md)，存储选型见 [ADR-0007](../../architecture/adr/0007-object-storage.md)，上传与下载流程见 [data-flow.md](../../architecture/data-flow.md#8-文件上传与下载)，表与 Redis 键见 [data-model.md](../../architecture/data-model.md#24-mediamarschat_media迁移-000001_init)。

| 层 | 位置 |
|---|---|
| 接口 | `api/media/v1/media.proto`、`internal.proto`、`error_reason.proto`；消息内容 `ImageContent`、`FileContent` 在 `api/message/v1/message.proto` |
| media | `app/media/internal/biz/file.go`（上传、下载权限、内部查询）、`image.go`（缩略图）、`filename.go`（文件名清理与响应头）；`internal/data/local.go`、`oss.go`（两种存储）；`internal/service/media.go`（自定义的上传与下载处理函数） |
| message | `app/message/internal/biz/message.go`（`resolveContent` 核实文件并覆盖元数据）、`internal/data/upstream.go`（调用 media） |

与旧基线的主要差别：

- 文件由服务端生成 ID，按内容识别类型（不信任扩展名和客户端声明的类型），文件名清理后只用于下载时的 `Content-Disposition`；旧基线按原文件名保存在静态目录、任何人拿到链接都能下载。
- 聊天文件需要访问密钥（随消息下发）或是上传者才能换取下载地址，地址 10 分钟有效；头像对所有登录用户公开。
- 图片消息有独立类型，带宽高和缩略图（最长边 320 像素）；旧基线所有文件都按文件卡片展示。
- 存储可切换：默认仓库下的本地目录，可选阿里云 OSS。

接口（HTTP 除下载内容外都需要登录，同名的 gRPC 方法见 proto）：

| 方法 | HTTP / 通道 | 说明 |
|---|---|---|
| `UploadFile` | `POST /v1/files?purpose=&name=` | 请求体是文件的原始字节（不是 multipart），必须带 `Content-Length`。`purpose` 为 `PURPOSE_CHAT`（最大 100 MiB）或 `PURPOSE_AVATAR`（最大 5 MiB，必须是 JPEG、PNG、GIF、WebP）；`name` 最长 1024 字节，清理后保留 255 字节。返回 `FileInfo`：`id`、`accessKey`、`kind`（`FILE_KIND_IMAGE` / `FILE_KIND_FILE`）、`name`、`size`、`mimeType`、`width`、`height`、`hasThumbnail` |
| `GetDownloadUrls` | `POST /v1/files/download-urls` | `items` 1～100 项，每项 `fileId`、`accessKey`（头像和自己上传的文件可以不填）、`variant`（`VARIANT_ORIGINAL` / `VARIANT_THUMBNAIL`）。返回有权限的项的 `url` 与 `expiresAt`，其余省略，不区分原因 |
| `DownloadContent` | `GET /v1/files/{file_id}/content?variant=&expires=&signature=` | 只在本地存储下使用，不需要登录，由签名授权，可以直接用于 `<img src>`；支持 `Range`。OSS 的下载地址指向 OSS，不经过 media |
| `MediaInternalService.GetFiles` | 仅 gRPC（内网） | message 发送图片、文件消息时调用，最多 100 个，不存在的 ID 跳过 |

图片、文件消息：客户端把 `FileInfo` 的 `id`、`accessKey` 填入 `ImageContent` / `FileContent` 经 WebSocket 发送，其余字段由 message 用 media 的记录覆盖。文件不存在、访问密钥不匹配、用途不是聊天，或图片消息引用的不是图片时返回 `400 FILE_INVALID`；media 不可用时返回 `503 DEPENDENCY_UNAVAILABLE`。转发时带上原消息中的文件 ID 和访问密钥即可，不要求发送者是上传者。

错误原因见 [media](../../../api/media/v1/error_reason.proto) 的 `error_reason.proto`：`411 LENGTH_REQUIRED`、`413 FILE_TOO_LARGE`（`metadata.max_bytes` 为上限）、`415 UNSUPPORTED_FILE_TYPE`、`400 UPLOAD_INCOMPLETE`（请求体没读完）、`403 DOWNLOAD_URL_INVALID`（签名错误或过期）、`429 RATE_LIMITED`（每人每小时上传 60 次）。

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
