# 联系人与申请

social 服务的好友与拉黑部分，设计与规则细节见 [social-service.md](../../design/social-service.md)，功能状态见 [features.md](../features.md)，表与 Redis 键见 [data-model.md](../../architecture/data-model.md#22-socialmarschat_social)。入群申请见 [group.md](group.md)。

| 层 | 位置 |
|---|---|
| 接口 | `api/social/v1/social.proto`、`internal.proto`、`error_reason.proto`、`events.proto` |
| 后端 | `app/social/internal/biz/friend.go`、`permission.go`；`app/social/internal/data/friend.go`、`account.go`（经 Consul 调用 account） |

要点：

- 当前用户取自访问令牌，只有接收方能处理申请。
- 申请保留历史，被拒后再申请生成新记录，对方能看到；已是好友时不能再申请。双方互相申请时直接成为好友。
- 拉黑是独立的单向关系，不删除好友关系；要拒绝骚扰的申请人，直接拉黑即可。
- 好友关系与拉黑不缓存（都是主键查询）。
- 实时通知：收到好友申请、成为好友、删除好友时，delivery 经 WebSocket 的 `Notification` 帧推给相关的在线用户，尽力推送，见 [data-flow.md](../../architecture/data-flow.md#54-好友与群通知)。拉黑不通知。

接口（全部需要登录，同名的 gRPC 方法见 proto；列表为游标分页，`cursor` 第一页传 0，`limit` 默认 20、最大 100）：

| 方法 | HTTP | 说明 |
|---|---|---|
| `SendFriendRequest` | `POST /v1/friend-requests` | `userId`、`message`；返回待处理申请，或 `becameFriends = true` |
| `ListReceivedFriendRequests` / `ListSentFriendRequests` | `GET /v1/friend-requests/received`、`/sent` | 收到的只有待处理的，发出的含全部状态；`peer` 为对方资料 |
| `AcceptFriendRequest` / `RejectFriendRequest` | `POST /v1/friend-requests/{request_id}/accept`、`/reject` | 仅接收方；同意时任一方已拉黑对方，申请改为已失效并返回 `403 FRIEND_REQUEST_NOT_ALLOWED` |
| `ListFriends` | `GET /v1/friends` | 带资料 |
| `DeleteFriend` | `DELETE /v1/friends/{user_id}` | 幂等 |
| `BlockUser` / `UnblockUser` / `ListBlockedUsers` | `POST /v1/blocks`、`DELETE /v1/blocks/{user_id}`、`GET /v1/blocks` | 幂等 |
| `SocialInternalService.CheckSendPermission` | 仅 gRPC（内网） | 单聊：先查拉黑（`BLOCKED`），再查好友（`NOT_FRIENDS`）；群聊见 group.md |

错误原因见 [error_reason.proto](../../../api/social/v1/error_reason.proto)，HTTP 状态码规则见 [system.md](../../architecture/system.md#58-错误)。
