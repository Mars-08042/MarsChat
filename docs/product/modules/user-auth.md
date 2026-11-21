# 用户与认证

account 服务，设计与规则细节见 [account-service.md](../../design/account-service.md)，功能状态见 [features.md](../features.md)，表与 Redis 键见 [data-model.md](../../architecture/data-model.md#21-accountmarschat_account)。账号标识是邮箱，验证码经 SMTP 发到邮箱，见 [email-verification.md](../../design/email-verification.md)。

| 层 | 位置 |
|---|---|
| 接口 | `api/account/v1/account.proto`、`internal.proto`、`error_reason.proto`、`events.proto` |
| 后端 | `app/account/internal/`（`biz` 业务规则，`data` 存储，`service` 接口适配，`server` 中间件与路由）；公共组件 `pkg/auth`、`pkg/idgen`、`pkg/cache`、`pkg/ratelimit`、`pkg/outbox` |

要点：用邮箱注册登录；密码用 bcrypt 哈希；登录后签发访问令牌（JWT，15 分钟）和按设备的刷新令牌，所有需要登录的接口都校验令牌；验证码有发送间隔、每日上限、IP 限流和验证次数上限；退出、踢设备、封禁后访问令牌立即失效。

接口（同名的 gRPC 方法见 proto）：

| 方法 | HTTP | 需要登录 | 说明 |
|---|---|---|---|
| `SendEmailCode` | `POST /v1/auth/email-codes` | 否 | `email`、`purpose`（`CODE_PURPOSE_REGISTER` / `CODE_PURPOSE_LOGIN`），返回 `cooldownSeconds` |
| `Register` | `POST /v1/auth/register` | 否 | `email`、`code`、`password`、`nickname`、`device`，返回用户与令牌对 |
| `LoginByPassword` | `POST /v1/auth/login/password` | 否 | `email`、`password`、`device` |
| `LoginByEmailCode` | `POST /v1/auth/login/email-code` | 否 | `email`、`code`、`device` |
| `RefreshToken` | `POST /v1/auth/refresh` | 否 | `refreshToken`，返回新的令牌对 |
| `Logout` | `POST /v1/auth/logout` | 是 | 吊销当前会话 |
| `GetMe` / `UpdateMe` | `GET` / `PATCH /v1/users/me` | 是 | 完整资料（含邮箱）；只修改请求中出现的字段，生日传空字符串表示清除 |
| `GetUser` | `GET /v1/users/{user_id}` | 是 | 公开资料 |
| `ListSessions` | `GET /v1/sessions` | 是 | 有效会话，`current` 标出当前设备 |
| `RevokeSession` | `DELETE /v1/sessions/{session_id}` | 是 | 踢掉自己的某个设备 |
| `AccountInternalService.BatchGetUsers` | 仅 gRPC | 否（内网） | 批量公开资料，不存在的 ID 跳过 |

`email` 最长 254 个字符，服务端去掉首尾空白并转为小写后使用，大小写不同的写法视为同一个邮箱；`code` 为 6 位数字。`device` 为 `deviceId`（客户端生成并持久保存，8～64 位字母数字、`_`、`-`）、`platform`、`deviceName`。错误原因与 HTTP 状态码见 [system.md](../../architecture/system.md#58-错误)。

头像：先经 media 上传（`POST /v1/files?purpose=PURPOSE_AVATAR`，见 [chat-message.md](chat-message.md#文件与图片media)），再用 `UpdateMe` 把返回的文件 ID（十进制字符串）写入 `avatar`。account 不调用 media 校验；显示时用 `GetDownloadUrls` 换取地址，用途为头像的文件对所有登录用户可读，填入别人的聊天文件 ID 换不到地址。群头像（social 的 `avatar` 字段）规则相同。
