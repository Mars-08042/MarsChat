# 群组

social 服务的群部分，设计与规则细节见 [social-service.md](../../design/social-service.md#34-群)，功能状态见 [features.md](../features.md)，表与 Redis 键见 [data-model.md](../../architecture/data-model.md#22-socialmarschat_social)。

| 层 | 位置 |
|---|---|
| 接口 | `api/social/v1/social.proto`、`internal.proto` |
| 后端 | `app/social/internal/biz/group.go`、`permission.go`；`app/social/internal/data/group.go`（群资料缓存、成员列表缓存、群 ID 布隆过滤器） |

要点：

- 成员存在 `group_members` 表，成员数在锁住群行的事务内维护，与成员行数一致；解散把状态改为已解散，群资料保留。
- 实时通知：建群、入群、退群或被移出、成员角色与禁言变化、群资料变化、解散时通知群成员，入群申请只通知群主和管理员；由 delivery 经 WebSocket 的 `Notification` 帧推送，尽力推送，见 [data-flow.md](../../architecture/data-flow.md#54-好友与群通知)。
- 角色分为群主、管理员、成员；支持邀请好友入群、建群时带初始成员、转让群主、单人禁言与全员禁言。转让后原群主变为管理员，新群主原有的禁言一并解除。
- 群资料修改用版本号乐观锁，两个管理员同时编辑时后提交的一方收到 `GROUP_VERSION_CONFLICT`。
- 没有禁用群的功能，系统管理员可以解散任意群。

权限：

| 操作 | 群主 | 管理员 | 成员 |
|---|---|---|---|
| 修改资料、全员禁言、审批入群申请、邀请好友 | 是 | 是 | 否 |
| 移除成员、单人禁言 | 是（管理员与成员） | 是（仅成员） | 否 |
| 设置或取消管理员、转让群主、解散 | 是 | 否 | 否 |
| 退群 | 先转让 | 是 | 是 |

接口（全部需要登录）：

| 方法 | HTTP | 说明 |
|---|---|---|
| `CreateGroup` | `POST /v1/groups` | `name`（1～30 字符）、`avatar`、`notice`、`joinMode`、`memberIds`（创建者的好友） |
| `GetGroup` | `GET /v1/groups/{group_id}` | 带 `myRole`；非成员看不到公告；已解散的群仍可查询 |
| `UpdateGroup` | `PATCH /v1/groups/{group_id}` | `version` 加要修改的字段，返回新资料 |
| `ListMyGroups` | `GET /v1/groups` | 按群 ID 分页，带 `myRole` |
| `JoinGroup` | `POST /v1/groups/{group_id}/join` | 返回 `joined = true`，或待处理的入群申请 |
| `ListGroupJoinRequests` | `GET /v1/groups/{group_id}/join-requests` | 群主、管理员 |
| `AcceptGroupJoinRequest` / `RejectGroupJoinRequest` | `POST /v1/group-join-requests/{request_id}/accept`、`/reject` | 群主、管理员 |
| `AddGroupMembers` | `POST /v1/groups/{group_id}/members` | 返回实际加入的 `addedUserIds` |
| `ListGroupMembers` | `GET /v1/groups/{group_id}/members` | 仅成员，带资料 |
| `RemoveGroupMember` | `DELETE /v1/groups/{group_id}/members/{user_id}` | |
| `SetGroupMemberRole` | `PUT /v1/groups/{group_id}/members/{user_id}/role` | `role` 只能是成员或管理员 |
| `MuteGroupMember` | `PUT /v1/groups/{group_id}/members/{user_id}/mute` | `durationSeconds`，0 为解除，最长 30 天 |
| `TransferGroupOwner` | `POST /v1/groups/{group_id}/transfer` | `newOwnerId` 必须是成员 |
| `LeaveGroup` | `POST /v1/groups/{group_id}/leave` | |
| `DismissGroup` | `POST /v1/groups/{group_id}/dismiss` | 群主或系统管理员 |
| `SocialInternalService.CheckSendPermission` | 仅 gRPC（内网） | 群聊：群正常、是成员、未被单人禁言；全员禁言时只有群主和管理员能发言 |
| `SocialInternalService.ListAllGroupMembers` / `BatchGetGroups` | 仅 gRPC（内网） | 全部成员（走缓存）；批量群资料，最多 100 个 |
