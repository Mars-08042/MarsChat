# 会话与消息

> **重构说明（2026-11-18）**：本文描述的是已移除的旧基线（截至提交 `6624cea`），其中的代码位置已不存在，只作功能参考。新实现见 [后端重构计划](../../plans/active/backend-refactor.md) 步骤 4 和 [ADR-0006](../../architecture/adr/0006-message-sync-model.md)。

## 职责

会话列表的创建与删除；单聊、群聊的实时消息与文件消息；历史消息查询。实时转发的内部机制见 [data-flow.md](../../architecture/data-flow.md#3-聊天消息流转)。

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
| `GET /wss?client_id=<uuid>` | — | 建立 WebSocket，消息格式见 [data-flow.md](../../architecture/data-flow.md#31-客户端--服务端消息格式) |
| `/chatroom/getCurContactListInChatRoom` | `owner_id`、`contact_id` | 恒返回空列表，前端未使用（TD-20） |

## 相关问题

TD-04（异常消息导致崩溃）、TD-08（连接生命周期）、TD-09（消息缓存无效）、TD-10（文件上传）、TD-14（重复代码）、TD-22（消息状态语义、无分页）。
