# 联系人与申请

> **重构说明（2026-11-18）**：本文描述的是已移除的旧基线（截至提交 `6624cea`），其中的代码位置已不存在，只作功能基线参考。新实现见 [后端重构计划](../../plans/active/backend-refactor.md) 步骤 3。

## 职责

好友与群的申请、审批、拉黑，好友列表与已加入群列表，删除好友。群本身的管理见 [group.md](group.md)。

## 代码位置

| 层 | 位置 |
|---|---|
| 后端 | `api/v1/user_contact_controller.go`、`internal/service/gorm/user_contact_service.go` |
| 前端 | `views/chat/contact/ContactList.vue`、`components/ContactListModal.vue`、`ContactChat.vue`（删除、拉黑操作） |

## 数据与状态

- `contact_apply`：申请记录，同一对 (申请人, 目标) 复用一条。
- `user_contact`：联系关系。好友双向各一条记录；群关系只有“用户 → 群”一条。

![申请与联系关系的状态](../../images/contact-state.svg)

状态取值见 [data-model.md](../../architecture/data-model.md#旧基线枚举)。

## 核心流程

1. **申请**：用户输入对方的用户 ID（`U...`）或群 ID（`G...`）及可选理由，调用 `/contact/applyContact`。目标被禁用时失败；好友申请曾被拉黑时失败。
2. **加群的两种方式**：前端先调用 `/group/checkGroupAddMode`。直接加入（`add_mode=0`）时调用 `/group/enterGroupDirectly`；需审核（`add_mode=1`）时走申请流程，由群主在聊天页处理。
3. **审批**：被申请人（或群主）通过 `/contact/getNewContactList`、`/contact/getAddGroupList` 查看待处理申请，可同意、拒绝或拉黑申请。同意好友申请会创建双向联系关系；同意加群申请会创建一条联系关系并把用户加入 `group_info.members`。
4. **拉黑 / 解除拉黑**：只改双方 `user_contact.status`，同时软删除操作者一方的会话。解除时要求双方状态确实是“拉黑 / 被拉黑”。
5. **删除好友**：双方联系关系标记为删除 / 被删除并软删除，双方会话与申请记录也软删除，之后可以重新申请。
6. **发起会话前检查**：`/session/checkOpenSessionAllowed` 会拒绝拉黑或被拉黑的关系，以及已被禁用的用户或群。

## 接口

| 路径 | 请求字段 | 说明 |
|---|---|---|
| `/contact/getUserList` | `owner_id` | 好友列表（带 Redis 缓存） |
| `/contact/loadMyJoinedGroup` | `owner_id` | 我加入的群（不含自己创建的，带缓存） |
| `/contact/getContactInfo` | `contact_id` | 用户或群资料；对方被禁用时返回业务错误 |
| `/contact/applyContact` | `owner_id`、`contact_id`、`message` | 申请加好友或加群 |
| `/contact/getNewContactList` | `owner_id` | 我收到的待处理好友申请 |
| `/contact/getAddGroupList` | `group_id` | 某群待处理的加群申请 |
| `/contact/passContactApply` | `owner_id`、`contact_id` | 同意。`owner_id` 为当前用户 ID（好友申请）或群 ID（加群申请），`contact_id` 为申请人 |
| `/contact/refuseContactApply` | 同上 | 拒绝 |
| `/contact/blackApply` | 同上 | 拉黑申请 |
| `/contact/deleteContact` | `owner_id`、`contact_id` | 删除好友 |
| `/contact/blackContact` | `owner_id`、`contact_id` | 拉黑 |
| `/contact/cancelBlackContact` | `owner_id`、`contact_id` | 解除拉黑 |

## 相关问题

TD-01（审批接口不校验操作者身份）、TD-09（缓存失效不完整）、TD-17（重复申请的状态处理）、TD-18（搜索框无过滤逻辑）、TD-19（状态注释与枚举不一致）。
