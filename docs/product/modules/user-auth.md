# 用户与认证

> **重构说明（2026-11-18）**：“新实现”一节描述 account 服务的当前代码；其后各节描述已移除的旧基线（截至提交 `6624cea`），其中的代码位置已不存在，只作功能参考。

## 新实现（重构步骤 2）

account 服务，设计与规则细节见 [account-service.md](../../design/account-service.md)，功能状态见 [features.md](../features.md#新后端)，表与 Redis 键见 [data-model.md](../../architecture/data-model.md#21-accountmarschat_account迁移-000001_init)。

| 层 | 位置 |
|---|---|
| 接口 | `api/account/v1/account.proto`、`internal.proto`、`error_reason.proto`、`events.proto` |
| 后端 | `app/account/internal/`（`biz` 业务规则，`data` 存储，`service` 接口适配，`server` 中间件与路由）；公共组件 `pkg/auth`、`pkg/idgen`、`pkg/cache`、`pkg/ratelimit`、`pkg/outbox` |

与旧基线的主要差别：密码用 bcrypt 哈希；登录后签发访问令牌（JWT，15 分钟）和按设备的刷新令牌，所有需要登录的接口都校验令牌；验证码有发送间隔、每日上限、IP 限流和验证次数上限；退出、踢设备、封禁后访问令牌立即失效。

接口（同名的 gRPC 方法见 proto）：

| 方法 | HTTP | 需要登录 | 说明 |
|---|---|---|---|
| `SendSmsCode` | `POST /v1/auth/sms-codes` | 否 | `phone`、`purpose`（注册 / 登录），返回 `cooldownSeconds` |
| `Register` | `POST /v1/auth/register` | 否 | `phone`、`smsCode`、`password`、`nickname`、`device`，返回用户与令牌对 |
| `LoginByPassword` | `POST /v1/auth/login/password` | 否 | `phone`、`password`、`device` |
| `LoginBySms` | `POST /v1/auth/login/sms` | 否 | `phone`、`smsCode`、`device` |
| `RefreshToken` | `POST /v1/auth/refresh` | 否 | `refreshToken`，返回新的令牌对 |
| `Logout` | `POST /v1/auth/logout` | 是 | 吊销当前会话 |
| `GetMe` / `UpdateMe` | `GET` / `PATCH /v1/users/me` | 是 | 完整资料（含手机号）；只修改请求中出现的字段，生日传空字符串表示清除 |
| `GetUser` | `GET /v1/users/{user_id}` | 是 | 公开资料 |
| `ListSessions` | `GET /v1/sessions` | 是 | 有效会话，`current` 标出当前设备 |
| `RevokeSession` | `DELETE /v1/sessions/{session_id}` | 是 | 踢掉自己的某个设备 |
| `AccountInternalService.BatchGetUsers` | 仅 gRPC | 否（内网） | 批量公开资料，不存在的 ID 跳过 |

`device` 为 `deviceId`（客户端生成并持久保存，8～64 位字母数字、`_`、`-`）、`platform`、`deviceName`。错误原因与 HTTP 状态码见 [system.md](../../architecture/system.md#57-错误)。

## 职责

手机号注册、登录（密码 / 短信验证码）、个人资料维护、退出登录。

## 代码位置

| 层 | 位置 |
|---|---|
| 后端 | `api/v1/user_info_controller.go`、`internal/service/gorm/user_info_service.go`、`internal/service/sms/auth_code_service.go` |
| 前端 | `views/access/Login.vue`、`SmsLogin.vue`、`Register.vue`；`views/chat/user/OwnInfo.vue`；`components/NavigationModal.vue`（退出）；`App.vue`（刷新后恢复） |

## 核心流程

**注册**：前端校验昵称 3～10 字符和手机号格式 → `/user/sendSmsCode` → 用户填写验证码 → `/register`。后端比对 Redis 中的 `auth_code_<手机号>`，校验通过后删除该键，检查手机号未被注册，创建用户（默认头像、`is_admin=0`、`status=0`）。

**登录**：密码登录直接比对明文密码；短信登录比对验证码。两种方式都返回用户信息，前端存入 Vuex 与 `sessionStorage`，随后建立 WebSocket 连接（见 [data-flow.md](../../architecture/data-flow.md#2-登录与建立长连接)）。

**短信验证码**：6 位数字，Redis 中保存 1 分钟；未过期时再次请求返回“请输入已发送的验证码”。

**登录态**：没有令牌或服务端会话。前端路由守卫只检查 `sessionStorage` 中有无 `userInfo.uuid`，后端各接口直接信任请求里的用户 ID（TD-01）。

**被禁用用户**：后端登录接口不拦截禁用用户；前端 `App.vue` 刷新时若发现 `status == 1` 会尝试自动退出，但该段代码会抛错（TD-18）。

## 接口

| 路径 | 请求字段 | 说明 |
|---|---|---|
| `/register` | `telephone`、`password`、`nickname`、`sms_code` | 注册并返回用户信息 |
| `/login` | `telephone`、`password` | 密码登录 |
| `/user/sendSmsCode` | `telephone` | 发送短信验证码 |
| `/user/smsLogin` | `telephone`、`sms_code` | 验证码登录 |
| `/user/getUserInfo` | `uuid` | 获取用户信息 |
| `/user/updateUserInfo` | `uuid`、`nickname`、`email`、`birthday`、`signature`、`avatar` | 只更新非空字段；性别、手机号、密码不可修改 |
| `/message/uploadAvatar` | multipart 文件 | 上传头像文件，之后把 `/static/avatars/<文件名>` 提交到上一个接口 |
| `/user/wsLogout` | `owner_id` | 关闭 WebSocket 并从在线表移除 |

## 相关问题

TD-01 无鉴权、TD-02 明文密码、TD-05 kafka 模式登出无效、TD-13 短信、TD-18 前端自动登出报错、TD-22 首个管理员需手动设置。
