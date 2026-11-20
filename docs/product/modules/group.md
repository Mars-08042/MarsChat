# 群组

> **重构说明（2026-11-19）**：“新实现”一节描述 social 服务的当前代码；其后各节描述已移除的旧基线（截至提交 `6624cea`），其中的代码位置已不存在，只作功能参考。

## 新实现（重构步骤 3）

social 服务的群部分，设计与规则细节见 [social-service.md](../../design/social-service.md#34-群)，功能状态见 [features.md](../features.md#新后端)，表与 Redis 键见 [data-model.md](../../architecture/data-model.md#22-socialmarschat_social迁移-000001_init)。

| 层 | 位置 |
|---|---|
| 接口 | `api/social/v1/social.proto`、`internal.proto` |
| 后端 | `app/social/internal/biz/group.go`、`permission.go`；`app/social/internal/data/group.go`（群资料缓存、成员列表缓存、群 ID 布隆过滤器） |

与旧基线的主要差别：

- 成员存在 `group_members` 表，成员数在锁住群行的事务内维护，与成员行数一致；解散把状态改为已解散（修正 TD-16）。
- 实时通知（重构步骤 5）：建群、入群、退群或被移出、成员角色与禁言变化、群资料变化、解散时通知群成员，入群申请只通知群主和管理员；由 delivery 经 WebSocket 的 `Notification` 帧推送，尽力推送，见 [data-flow.md](../../architecture/data-flow.md#54-好友与群通知)。
- 新增管理员角色、邀请好友入群、建群时带初始成员、转让群主、单人禁言与全员禁言。
- 群资料修改用版本号乐观锁，两个管理员同时编辑时后提交的一方收到 `GROUP_VERSION_CONFLICT`。
- 管理员禁用群的功能不做，系统管理员可以解散任意群。

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

## 职责

创建群聊、维护群资料、成员管理、退群与解散。入群申请流程见 [contact.md](contact.md)，管理员对群的禁用与删除见 [admin.md](admin.md)。

## 代码位置

| 层 | 位置 |
|---|---|
| 后端 | `api/v1/group_info_controller.go`、`internal/service/gorm/group_info_service.go` |
| 前端 | `components/ContactListModal.vue`（建群）、`views/chat/contact/ContactChat.vue`（群资料、成员、退群、解散） |

## 数据要点

- 成员列表以 JSON 数组存在 `group_info.members`，`member_cnt` 为冗余计数；每个成员另有一条 `user_contact`（`contact_type=1`）。
- 创建者即群主（`owner_id`），群主身份不能转让。
- 加群方式 `add_mode`：0 直接加入，1 需群主审核。

## 核心流程

- **建群**：生成 `G...` ID，成员只有群主，同时为群主创建联系关系。群主的群出现在“我创建的群”，不出现在“我加入的群”。
- **修改资料**：名称、公告、头像、加群方式（`add_mode = -1` 表示不修改）；同时同步所有相关会话的名称与头像。
- **移除成员**：群主不能被移除；被移除者的会话、联系关系、申请记录软删除。
- **退群**：从成员列表移除，联系关系标记为“退出群聊”并软删除，会话与申请记录软删除。
- **解散**：群、全部会话、全部联系关系、全部申请记录软删除。`status` 不会改为“解散”。

前端负责只对群主显示管理操作，后端不校验调用者是否为群主（TD-01）。

## 接口

| 路径 | 请求字段 | 说明 |
|---|---|---|
| `/group/createGroup` | `owner_id`、`name`、`notice`、`add_mode`、`avatar` | 建群 |
| `/group/loadMyGroup` | `owner_id` | 我创建的群（带缓存） |
| `/group/getGroupInfo` | `group_id` | 群详情（含 `is_deleted`） |
| `/group/updateGroupInfo` | `owner_id`、`uuid`、`name`、`notice`、`avatar`、`add_mode` | 修改群资料 |
| `/group/checkGroupAddMode` | `group_id` | 查询加群方式 |
| `/group/enterGroupDirectly` | `owner_id`、`contact_id` | 直接入群。**注意 `owner_id` 是群 ID，`contact_id` 是入群用户** |
| `/group/getGroupMemberList` | `group_id` | 成员列表 |
| `/group/removeGroupMembers` | `group_id`、`owner_id`、`uuid_list` | 移除成员 |
| `/group/leaveGroup` | `user_id`、`group_id` | 退群 |
| `/group/dismissGroup` | `owner_id`、`group_id` | 解散 |

## 相关问题

TD-01、TD-09（多处缓存失效无效）、TD-16（错误判断写反、成员计数、解散状态）、TD-20（禁言状态未实现）。
