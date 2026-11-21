# 设计：用邮箱验证码替代手机短信

- 状态：已采纳（2026-11-20 开发者确认，第 8 节 5 个问题均按推荐方案；同日实现，实施中的调整见第 9 节） | 日期：2026-11-20 | 关联：[account 设计](account-service.md)、[user-auth.md](../product/modules/user-auth.md#新实现重构步骤-2)、[技术债](../plans/tech-debt.md)（TD-24、TD-29、TD-35、TD-36）、[重构计划](../plans/active/backend-refactor.md)

## 1. 背景与目标

account 从步骤 2 起用“手机号 + 短信验证码”注册和登录，但只有把验证码写进日志的 `log` 发送方（TD-24）。2026-11-20 核对阿里云控制台后确认：

- 已开通的“短信服务”套餐包需要签名实名制报备。控制台写明“短信服务不支持‘个人资质’的签名实名制报备”，开发者账号是个人认证，无法申请签名；测试专用签名也已取消。
- 免资质的“号码认证服务·短信认证”需要单独开通、单独付费，还要配置 RAM 用户和赠送签名、模板。

对一个演示项目来说，短信的接入成本过高。开发者决定改用邮箱：注册和登录都以邮箱为账号，验证码发到邮箱。

目标：

- 邮箱取代手机号，作为账号的唯一标识：邮箱加验证码注册、邮箱加密码登录、邮箱加验证码登录。
- 实现真实的邮件发送方（SMTP），开发和自动化测试继续使用 `log` 发送方与固定验证码。
- 验证码的防刷规则不变：按 IP 限流、同一邮箱的发送间隔与每日上限、验证次数上限、验证后立即失效。
- 顺带修复与注册、登录链路相关的小型技术债。

不做：

- 手机号绑定、找回密码、修改邮箱。旧基线没有这些功能，计划中也没有。
- HTML 邮件模板、邮件退信处理。

## 2. 需求与约束

- 个人开发者能独立完成配置，不需要企业资质或域名。
- 不新增 Go 依赖：标准库 `net/smtp`、`crypto/tls`、`mime` 足够。
- `dev` 分支还没有推送到远端，接口没有对外发布过，可以直接改名，不保留兼容字段。CI 的 `buf breaking` 在分支首次推送时跳过。
- 开发库里已有测试数据，social、message 中有引用这些用户 ID 的数据。表结构变更写 SQL 迁移。
- account 的 HTTP 和 gRPC 超时都是 3 秒，发信要在这个时间内完成或失败。

## 3. 方案

### 3.1 发信方式

用 SMTP 直接经个人邮箱发信：

- 在邮箱设置中开启 SMTP 服务并生成授权码。授权码只用于第三方客户端登录，不是邮箱密码。
- 常见配置为 `smtp.qq.com:465` 或 `smtp.163.com:465`，即隐式 TLS。也支持 587 端口的 STARTTLS。
- 发件人必须是登录的邮箱地址，个人邮箱有每日发送上限，演示和自测足够。

实现在 `app/account/internal/data` 中，新的 `smtpMailer` 实现现有的发送方接口：

1. 按配置用隐式 TLS 或 STARTTLS 建立连接，校验服务器证书，用 `PLAIN` 认证。
2. 整个会话设置一个截止时间：取请求剩余截止时间与配置的 `mail.smtp.timeout`（默认 2.5 秒）中较小的一个。超时按发送失败处理。
3. 邮件为纯文本：
   - 头部带 `From`（显示名 MarsChat）、`To`、`Subject`（按 RFC 2047 编码中文）、`Date`、`Message-ID`、`MIME-Version`；
   - 正文 UTF-8，base64 编码。
   - 正文示例：“你的 MarsChat 注册验证码是 123456，5 分钟内有效。如果不是你本人操作，请忽略本邮件。”有效期取热更新的 `code_ttl`。

发送失败时沿用现有处理：撤销发送名额、删除已保存的验证码，返回 `503 CODE_SEND_FAILED`，用户可以立即重试。日志只记录脱敏后的邮箱（`a***@example.com`）。

### 3.2 账号标识

邮箱完全取代手机号，不保留手机号字段：

- 校验：protovalidate 的 `string.email` 加最大长度 254。服务端去掉首尾空白并整体转小写后再使用，存储、Redis 键、唯一性都按小写后的值。
- 唯一性：`users.email` 唯一索引。表的排序规则 `utf8mb4_0900_ai_ci` 本身不区分大小写，转小写是为了让 Redis 键与数据库一致。
- 公开资料（`UserProfile`）不含邮箱，与原来不含手机号一致。

### 3.3 防刷规则

规则与原来相同，只是计数对象从手机号换成邮箱：

- 按 IP 限流：每 IP 每小时 20 次，Redis 故障时拒绝；
- 同一邮箱两次发送间隔 60 秒，每日上限 10 次；
- 验证码 5 分钟有效，最多验证 5 次，验证后立即失效；
- 密码登录 15 分钟内失败 5 次锁定 15 分钟。

这些限额都在 `policy` 段热更新，字段名从 `sms` 改为 `email_code`。邮件不按条计费，但个人邮箱有发送上限，被刷爆会影响正常用户，所以 Redis 故障时仍然拒绝发送。

## 4. 接口、配置与数据变化

### 4.1 接口（`api/account/v1`，接口协议变化）

| 原来 | 改为 |
|---|---|
| `SendSmsCode`，`POST /v1/auth/sms-codes`，`phone`、`purpose` | `SendEmailCode`，`POST /v1/auth/email-codes`，`email`、`purpose` |
| `Register`：`phone`、`sms_code`、`password`、`nickname`、`device` | `email`、`code`、`password`、`nickname`、`device` |
| `LoginByPassword`：`phone`、`password`、`device` | `email`、`password`、`device` |
| `LoginBySms`，`POST /v1/auth/login/sms`，`phone`、`sms_code` | `LoginByEmailCode`，`POST /v1/auth/login/email-code`，`email`、`code` |
| `User.phone`（字段 2） | `User.email`（字段 2） |
| 枚举 `SmsPurpose` | `CodePurpose`（值不变：注册、登录） |

错误原因按原编号改名：

| 原来 | 改为 | HTTP |
|---|---|---|
| `SMS_SEND_TOO_FREQUENT` | `CODE_SEND_TOO_FREQUENT` | 429 |
| `SMS_DAILY_LIMIT_EXCEEDED` | `CODE_DAILY_LIMIT_EXCEEDED` | 429 |
| `SMS_SEND_FAILED` | `CODE_SEND_FAILED` | 503 |
| `SMS_CODE_INVALID` | `CODE_INVALID` | 400 |
| `SMS_CODE_ATTEMPTS_EXCEEDED` | `CODE_ATTEMPTS_EXCEEDED` | 400 |
| `PHONE_ALREADY_REGISTERED` | `EMAIL_ALREADY_REGISTERED` | 409 |

事件（`events.proto`）不含手机号，不受影响。

### 4.2 配置（`app/account/internal/conf/conf.proto`）

```yaml
mail:
  provider: log            # log | smtp
  fixed_code: ${MAIL_FIXED_CODE:000000}   # 只允许与 log 一起使用，沿用原有校验
  smtp:
    host: ${SMTP_HOST:}
    port: ${SMTP_PORT:465}
    username: ${SMTP_USERNAME:}           # 同时作为发件人地址
    password: ${SMTP_PASSWORD:}           # 邮箱的 SMTP 授权码，只放在 .env
    implicit_tls: true                    # 465 为 true，587（STARTTLS）为 false
    timeout: 2.5s

policy:
  email_code:              # 原 policy.sms，字段不变，daily_limit_per_phone 改为 daily_limit_per_email
    code_ttl: 300s
    ...
```

- `provider: smtp` 时 host、username、password 必填，启动时校验，缺少时启动失败。
- 授权码只写进被忽略的 `.env`，`.env.example` 中只写变量名。
- `provider` 切换需要重启；`policy.email_code` 热更新。

### 4.3 数据

迁移 `app/account/migrations/000002_email.up.sql`：

```sql
ALTER TABLE users ADD COLUMN email VARCHAR(254) NULL AFTER id;
-- 已有的开发数据填占位邮箱：.invalid 是保留顶级域，不会真的发出邮件
UPDATE users SET email = CONCAT('u', id, '@legacy.invalid');
ALTER TABLE users
  MODIFY email VARCHAR(254) NOT NULL,
  ADD UNIQUE KEY uk_email (email),
  DROP INDEX uk_phone,
  DROP COLUMN phone;
```

- 已有测试用户保留 ID，social、message 中的好友、群、消息不受影响。它们改用占位邮箱，可以用原密码登录，但收不到验证码。
- down 迁移恢复 `phone` 列和唯一索引，手机号无法还原，用占位值填充。

Redis 键（同步更新 data-model.md）：

| 原来 | 改为 |
|---|---|
| `account:sms:code:{purpose}:{phone}` | `account:email_code:code:{purpose}:{email}` |
| `account:sms:cooldown:{phone}` | `account:email_code:cooldown:{email}` |
| `account:sms:daily:{phone}:{day}` | `account:email_code:daily:{email}:{day}` |
| `account:login_fail:{phone}` | `account:login_fail:{email}` |
| `account:user:{id}`（缓存的 JSON 含 `phone`） | `account:user:v2:{id}`（含 `email`） |

旧键都有 TTL，自然过期，不需要手动清理。资料缓存的键加版本号，是为了避免升级后读到不含邮箱的旧缓存。

### 4.4 其他

- 代码中 `Sms*`、`Phone*` 命名统一改为 `Code*`/`Email*`，例如 `SmsUsecase` → `CodeUsecase`、`MaskPhone` → `MaskEmail`、`ErrPhoneTaken` → `ErrEmailTaken`。
- `test/testkit` 的 `RandomPhone` 改为 `RandomEmail`，生成 `e2e-<随机串>@example.test`；e2e 与集群测试随之修改。
- `make grant-admin PHONE=…` 改为 `EMAIL=…`，`scripts/grant-admin.sh` 按邮箱查用户。
- `make gen` 重新生成 proto 代码与 `api/openapi.yaml`。

## 5. 候选方案与权衡

| 方案 | 优点 | 缺点 | 结论 |
|---|---|---|---|
| A. 个人邮箱 SMTP（标准库） | 不需要资质和域名，几分钟配好；不新增依赖 | 个人邮箱有每日发送上限；发件地址是个人邮箱 | 采用 |
| B. 阿里云邮件推送（DirectMail） | 发信量大，可以自定义发信域名 | 需要自己的域名并配置 SPF、DKIM 等 DNS 记录；要引入 SDK 或另做签名 | 不采用，以后需要时只替换发送方 |
| C. 号码认证服务·短信认证 | 仍然用手机号，符合国内习惯 | 需要单独开通付费、配置 RAM 用户和赠送签名 | 开发者认为配置太麻烦，放弃 |
| D. 邮箱与手机号并存 | 两种方式都能登录 | 表结构、接口、防刷规则都要维护两套；短信仍然发不出去 | 不采用 |

本地测试是否引入 Mailpit（模拟 SMTP 服务器的容器，带网页查看邮件）：

- 推荐不引入：SMTP 发送方用测试内的最小假 SMTP 服务器做单元测试，覆盖 TLS、认证和邮件格式；e2e 继续用 `log` 发送方与固定验证码。
- 引入的好处是 e2e 能经过真实的 SMTP 链路，代价是多一个镜像和容器。

## 6. 风险与验证

| 风险 | 应对 |
|---|---|
| SMTP 握手加认证超过 3 秒的请求超时 | 发信截止时间不超过请求剩余时间，超时按发送失败处理并撤销名额。真实发送时记录耗时；如果经常超过 2 秒，再改为异步发送（立即返回，失败只记日志） |
| 邮件被收件方判为垃圾邮件 | 发件人与登录邮箱一致，头部完整（`Date`、`Message-ID`）；手动验证时检查收件箱和垃圾箱 |
| 邮件头注入 | 收件地址经 protovalidate 邮箱校验，不含换行；主题和正文由服务端生成，不包含用户输入 |
| 授权码泄露 | 只放在 `.env`；日志不打印配置；泄露后在邮箱设置中作废并重新生成授权码即可 |
| 用户资料缓存读到旧结构 | 缓存键加版本号 |

验证：

- 单元测试：
  - biz 层验证码流程与原测试一一对应；
  - 邮箱规范化（空白、大小写）；
  - 邮件内容（主题编码、正文、头部）；
  - SMTP 发送方对接假 SMTP 服务器：隐式 TLS、认证失败、超时、服务器拒收；
  - 迁移在 MySQL 中执行一次，确认已有数据被正确填充。
- e2e 与集群测试改用邮箱后全部通过。
- 手动验证：开发者在 `.env` 中配置 SMTP 后，经网关给自己的邮箱发一封注册验证码，用它完成注册和验证码登录，并记录发信耗时。

## 7. 实施顺序

1. proto、配置 proto、迁移；`make gen`。
2. account：biz 改名与邮箱规范化，data 层 Redis 键、用户表、缓存键，`log` 与 `smtp` 发送方，单元测试。
3. testkit、e2e、集群测试、`grant-admin`。
4. 技术债：TD-35（发号失败返回 503）、TD-36（go-redis 内部日志转到 `slog`）、TD-29（同意好友申请时检查拉黑）。
5. 文档：features、user-auth、data-model、deployment（SMTP 配置与授权码获取）、system、account 设计第 8 节；TD-24 改为“不再适用”移到已解决。
6. 验证：`buf lint` → `go vet`（含 `e2e`、`cluster` 标签）→ `go build` → `go test -race` → 迁移 → 宿主机模式 e2e → 重建镜像后 `make cluster-test` → 手动真实发信。

## 8. 待确认问题

1. **账号标识**：推荐邮箱完全取代手机号（方案 A）。备选是邮箱与手机号并存（方案 D），代价是两套规则。
2. **发信方式**：推荐个人邮箱 SMTP。开发者需要准备一个开启了 SMTP 服务的邮箱和授权码（QQ 邮箱或 163 邮箱均可）。
3. **已有开发数据**：推荐迁移时为已有用户填占位邮箱，保留关联数据。备选是清空开发库后重新迁移，迁移脚本更简单，但 social、message 的测试数据也要一起清掉。
4. **本地 SMTP 测试**：推荐不引入 Mailpit，用测试内的假 SMTP 服务器。
5. **技术债**：推荐同时修复 TD-35、TD-36、TD-29；TD-24 随本方案关闭。

## 9. 实施说明

实现与上文的差别：

| 项目 | 设计 | 实现与原因 |
|---|---|---|
| SMTP 配置 | `host`、`port`、`implicit_tls` 三个字段 | 合成一个 `addr`（`host:port`），465 端口自动使用隐式 TLS，其他端口要求 STARTTLS。两个字段可能互相矛盾（例如 465 配 `implicit_tls: false`），不如由端口决定 |
| 发送方切换 | `provider: log` 写死在配置文件中 | `provider: ${MAIL_PROVIDER:log}`，经 `.env` 切换，不用改动受版本控制的配置文件；compose 把 `MAIL_*`、`SMTP_*` 变量传进 account 容器 |
| 发信实现名称 | `smtpMailer` | `smtpSender`，与 `logCodeSender` 一起实现 `biz.CodeSender` |
| 保留域名 | 未提及 | 发往 `.test`、`.example`、`.invalid`、`.localhost`、`example.com/net/org` 时直接按发送失败处理：误用 `smtp` 发送方跑 e2e 时，测试账号（`@example.test`）和占位邮箱（`@legacy.invalid`）不会消耗个人邮箱的每日发信额度 |
| 收件地址 | 未提及 | 再挡一次 CR/LF，防止邮件头注入（请求已经过 `string.email` 校验，这里是纵深防御） |
| IP 限流键 | 未列出 | `account:rl:sms_ip:{ip}` 改为 `account:rl:email_code_ip:{ip}`，集群测试清理计数时同步修改 |
| `MaskEmail` | `a***@example.com` | 同设计：保留首字符和完整域名；`@` 在开头或结尾等异常格式整体输出 `***` |
| 测试设备 ID | 未提及 | e2e 原来用手机号拼设备 ID，邮箱中的 `@`、`.` 不满足设备 ID 的字符规则，改用邮箱的本地部分（`testkit.LocalPart`） |
| TD-35 | 发号失败返回 503 | 同设计，另外发现访问日志对所有 5xx 记 ERROR，503 记 WARN 需要同时修改 `accesslog`；account 注册改为先发号再校验验证码，租约失效时不消耗验证码 |

| 示例端口 | 3.1 节以 465 为常见配置 | `.env.example` 与 deployment 改为 `smtp.qq.com:587`：开发机到 465 端口的 TLS 连接被截获（证书被替换为自签证书），587 正常，见 [lessons/smtp-465-tls-interception.md](../lessons/smtp-465-tls-interception.md)。发送方拒绝了被替换的证书，授权码没有发出 |
| 同步发送的耗时 | 第 6 节：经常超过 2 秒再改为异步 | 实测 1.2～1.5 秒（QQ 邮箱，587），保持同步发送 |

验证结果见[重构计划](../plans/active/backend-refactor.md#当前进度与下一步)的“插入任务：邮箱验证码”。
