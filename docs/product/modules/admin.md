# 后台管理

> **重构说明（2026-11-18）**：本文描述的是已移除的旧基线（截至提交 `6624cea`），其中的代码位置已不存在，只作功能参考。新实现见 [后端重构计划](../../plans/active/backend-refactor.md) 步骤 2。

## 职责

管理员对用户和群聊的管控：启用 / 禁用、删除、设置管理员。

## 代码位置

| 层 | 位置 |
|---|---|
| 后端 | `api/v1/user_info_controller.go`、`group_info_controller.go`；`user_info_service.go`、`group_info_service.go` 中标注“管理员”的方法 |
| 前端 | `views/manager/Manager.vue`；`components/DisableUserModal.vue`、`DeleteUserModal.vue`、`SetAdminModal.vue`、`DisableGroupModal.vue`、`DeleteGroupModal.vue` |

## 权限

- 导航栏只对 `is_admin == 1` 的用户显示“管理”入口（`NavigationModal.vue`）。
- **后端所有管理接口都不校验调用者身份**，知道接口即可调用（TD-01）。
- 注册用户默认不是管理员，首个管理员需要直接改数据库（旧部署文档，见提交 `6624cea` 中的 `docs/deployment.md`）。

## 功能与副作用

| 操作 | 副作用 |
|---|---|
| 禁用用户 | `status=1`，软删除该用户参与的全部会话；对方无法再与其发起会话 |
| 启用用户 | `status=0`，已删除的会话不会恢复 |
| 删除用户 | 软删除用户、会话、联系关系、申请记录 |
| 设置 / 取消管理员 | 修改 `is_admin`；多选时可能失败（TD-15） |
| 禁用群聊 | `status=1`，软删除该群的全部会话 |
| 启用群聊 | `status=0` |
| 删除群聊 | 软删除群、会话、联系关系、申请记录 |

用户与群聊列表包含已软删除的记录（`is_deleted` 字段），管理端列表不走 Redis 缓存。

## 接口

| 路径 | 请求字段 | 说明 |
|---|---|---|
| `/user/getUserInfoList` | `owner_id` | 除自己外的全部用户 |
| `/user/ableUsers` | `uuid_list` | 启用 |
| `/user/disableUsers` | `uuid_list` | 禁用 |
| `/user/deleteUsers` | `uuid_list` | 删除 |
| `/user/setAdmin` | `uuid_list`、`is_admin` | 设置或取消管理员 |
| `/group/getGroupInfoList` | — | 全部群聊 |
| `/group/setGroupsStatus` | `uuid_list`、`status` | 启用或禁用 |
| `/group/deleteGroups` | `uuid_list` | 删除 |

## 相关问题

TD-01、TD-09（删除群聊未清理“我加入的群”缓存）、TD-15、TD-16。
