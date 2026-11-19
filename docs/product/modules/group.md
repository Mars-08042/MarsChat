# 群组

> **重构说明（2026-11-18）**：本文描述的是已移除的旧基线（截至提交 `6624cea`），其中的代码位置已不存在，只作功能基线参考。新实现见 [后端重构计划](../../plans/active/backend-refactor.md) 步骤 3。

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
