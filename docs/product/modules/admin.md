# 后台管理

系统管理员的操作分在两个服务：封禁与解封用户在 account（`app/account/internal/biz/admin.go`），解散任意群在 social（`DismissGroup`，见 [group.md](group.md)）。没有单独的管理后台服务，管理界面随前端重构实现。

| 方法 | HTTP | 说明 |
|---|---|---|
| `BanUser` | `POST /v1/admin/users/{user_id}/ban` | 修改状态、吊销全部会话、写 `UserBanned` 事件；提交后写入用户级吊销标记，已签发的访问令牌立即失效。重复封禁幂等 |
| `UnbanUser` | `POST /v1/admin/users/{user_id}/unban` | 恢复状态；用户需要重新登录 |

- 权限：令牌中的角色必须是管理员，否则返回 `403 PERMISSION_DENIED`；不能封禁自己。
- 设置管理员没有接口，用 `make grant-admin EMAIL=...`（见 [deployment.md](../../deployment.md#设置管理员)），新角色在该用户下次刷新令牌后生效。
- 被封禁的用户登录时，密码正确才返回 `USER_BANNED`，密码错误仍返回 `INVALID_CREDENTIALS`，不向未认证的人暴露账号状态。
