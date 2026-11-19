# 设计：account 服务（重构步骤 2）

- 状态：已实施（2026-11-18 开发者确认，同日实现，实施中的调整见第 8 节） | 日期：2026-11-18 | 关联：[重构计划步骤 2](../plans/active/backend-refactor.md#2-account-服务)、[ADR-0005](../architecture/adr/0005-distributed-architecture.md)、旧基线 [user-auth.md](../product/modules/user-auth.md)、[admin.md](../product/modules/admin.md)

## 1. 背景与目标

旧基线没有鉴权（TD-01）、密码明文存储（TD-02），短信验证码只有 1 分钟有效期，没有任何防刷手段。步骤 2 实现 account 服务，并补上步骤 1 移过来的公共能力。

目标：

- 手机号加短信验证码注册；密码登录、短信验证码登录；刷新令牌、退出；个人资料；设备列表与踢设备；管理员封禁与解封。
- 公共能力：接口 proto 生成、protovalidate、错误码、snowflake ID、SQL 迁移、缓存组件、限流组件、JWT 中间件、Outbox 写入。
- 每个机制对应一个具体的生产问题：短信成本与轰炸、密码暴力破解、令牌被盗、缓存穿透、多实例 ID 冲突、写库与发事件不一致。

不在本步骤：头像上传（步骤 6 由 media 提供文件 ID）、Outbox 发布到 Kafka（步骤 4）、注销账号、设置管理员的接口。

## 2. 需求与约束

- 手机号只支持中国大陆 11 位号码（阿里云国内短信）。
- 同一用户可以在多个设备上同时登录；同一设备重复登录时替换旧会话。
- 退出、踢设备、封禁后，已签发的访问令牌要立即失效，不能等到过期。
- 其他服务只能校验令牌，不能签发令牌。
- 接口同时提供 HTTP（经网关给前端）和 gRPC（给其他服务）；内部接口只开放 gRPC。
- int64 ID 在 JSON 中按字符串输出（protojson 默认行为），前端不会丢失精度。

## 3. 方案

### 3.1 模块划分

| 位置 | 内容 |
|---|---|
| `api/account/v1/` | `account.proto`（对外接口）、`internal.proto`（内部 gRPC）、`error_reason.proto`、`events.proto`（Outbox 事件） |
| `app/account/internal/` | `biz`（注册、登录、令牌、资料、会话、封禁）、`data`（repo、缓存）、`service`、`server` |
| `app/account/migrations/` | 版本化 SQL，`migrate/migrate` 镜像执行 |
| `pkg/auth` | 令牌声明、签发（只有 account 使用）、校验中间件（各服务复用）、从 context 取当前用户 |
| `pkg/idgen` | snowflake，节点号用 Redis 租约分配 |
| `pkg/cache` | Cache-Aside 组件：空值缓存、TTL 抖动、singleflight、Redis 故障时直接读库 |
| `pkg/ratelimit` | Redis 滑动窗口限流（ZSET + Lua） |
| `pkg/outbox` | Outbox 表的事务内写入；发布器在步骤 4 实现 |

### 3.2 令牌

**访问令牌**：JWT，Ed25519（EdDSA）签名，有效期 15 分钟。account 持有私钥，其他服务只配置公钥，即使其他服务被攻破也无法伪造令牌。头部带 `kid`，为轮换密钥预留。

| 声明 | 含义 |
|---|---|
| `sub` | 用户 ID |
| `sid` | 会话 ID（一台设备一次登录） |
| `role` | 1 普通用户、2 管理员（与 `account.v1.Role` 一致） |
| `iss` / `aud` | `marschat-account` / `marschat` |
| `iat` / `exp` | 签发时间、过期时间 |

**刷新令牌**：格式为 `<会话ID>.<32 字节随机数的 base64url>`，有效期 30 天，每次刷新顺延。数据库只存随机部分的 SHA-256。

- **轮换**：每次刷新都签发新的刷新令牌。用 `UPDATE ... WHERE id = ? AND refresh_hash = ?` 原子替换哈希，影响行数为 0 说明已被别的请求换掉。
- **重放检测**：提交的令牌与当前哈希不符，但与上一次的哈希相符：
  - 距离上次轮换不到 10 秒，视为客户端并发刷新（如多个标签页），返回 `REFRESH_TOKEN_ROTATED`，客户端改用本地存储里最新的令牌，不吊销会话。
  - 超过 10 秒，视为令牌泄露后被重放，立即吊销整个会话。
  - 与两个哈希都不符，返回 `REFRESH_TOKEN_INVALID`。
- 刷新时同时检查用户状态，已封禁的用户不能刷新。

**吊销名单**（Redis，所有服务读取）：

- 退出、踢设备：`auth:revoked:sid:{sid}`，TTL 等于访问令牌有效期，名单不会无限增长。
- 封禁：`auth:revoked:user:{uid}` 记录封禁时间，`iat` 早于它的令牌全部拒绝。解封后重新登录签发的令牌 `iat` 更晚，不受影响。
- 中间件每个请求用一次 `MGET` 同时查这两个键。

**中间件** `pkg/auth.Server`：Kratos JWT contrib 负责验签与解析（自定义声明类型），再检查吊销名单，最后把当前用户放进 context。公开接口（发验证码、注册、登录、刷新）用 Kratos `selector` 跳过。管理接口另外检查 `role` 声明。

- 读 contrib 源码（`tmp/kratos`，提交 `dd0fb7d`）发现：它先按令牌头部声明的算法解析，解析完才比对签名算法。所以要通过 `WithParserOptions` 传入 `jwt.WithValidMethods([]string{"EdDSA"})`，在解析阶段就拒绝其他算法，防止算法混淆攻击；同时传入签发方、受众校验，并要求令牌必须带 `exp`。
- contrib 的所有鉴权错误都是 401，原因为 `UNAUTHORIZED`，不区分过期和无效。吊销检查失败时也返回同样的原因。客户端的处理统一为：收到 401 先刷新一次，刷新失败再回到登录页。

### 3.3 短信验证码

| 规则 | 实现 | 默认值 |
|---|---|---|
| 用途隔离 | 注册码和登录码分开存，不能混用 | — |
| 有效期 | `account:sms:code:{purpose}:{phone}`，哈希表存验证码的 SHA-256 和已尝试次数 | 5 分钟 |
| 发送间隔 | `account:sms:cooldown:{phone}`，`SET NX EX` | 60 秒 |
| 每号每日上限 | `account:sms:daily:{phone}:{yyyymmdd}`，`INCR` | 10 次 |
| 每 IP 限流 | `pkg/ratelimit` 滑动窗口 | 每小时 20 次 |
| 验证次数上限 | Lua 脚本原子地比对并累加次数，超过即删除验证码 | 5 次 |
| 一次性使用 | 验证成功的同一个 Lua 脚本里删除验证码 | — |

表中的次数和时长是 `policy.sms` 的默认值，可以热更新，见 3.7。

- 发送注册码时手机号已注册，返回 `PHONE_ALREADY_REGISTERED`；发送登录码时未注册，返回 `USER_NOT_FOUND`。这会暴露手机号是否注册，与常见国内应用一致，由上面的限流控制枚举速度。
- 发送失败（短信平台报错）时回滚冷却键和日计数，用户可以立即重试。
- 发送方抽象为接口，有两个实现：
  - `log`：只写日志，开发与测试用。可配置固定验证码供 e2e 测试使用，配置了固定验证码却选择 `aliyun` 时拒绝启动。
  - `aliyun`：阿里云短信。开发者暂时没有 AccessKey、签名和模板，本步骤不实现，也不引入 SDK；配置就绪后补上并验证。
- 日志中手机号脱敏为 `138****8000`。

### 3.4 密码登录防护

- 密码用 bcrypt 哈希，cost 为 12。长度 8～64 位，必须同时包含字母和数字。bcrypt 只处理前 72 字节，64 位上限可以保证全部参与计算。
- 同一手机号 15 分钟内失败 5 次后锁定 15 分钟（`account:login_fail:{phone}`）；每 IP 每分钟最多 20 次登录请求。这些都是 `policy.login` 的默认值，可以热更新。
- 手机号不存在或密码错误统一返回 `INVALID_CREDENTIALS`。手机号不存在时也用一个固定哈希跑一次 bcrypt，让两种情况耗时相同。

### 3.5 用户资料缓存

用 `pkg/cache` 实现 Cache-Aside，键为 `account:user:{id}`，值为 JSON 编码的资料（不含密码哈希）。

| 问题 | 措施 |
|---|---|
| 穿透 | 先用参数校验挡掉非法 ID；不存在的 ID 缓存空值标记，TTL 1 分钟；用布隆过滤器 `account:user_bf` 判断 ID 是否存在 |
| 击穿 | 进程内用 singleflight 合并同一个键的并发回源 |
| 雪崩 | TTL 30 分钟，加 ±10% 随机抖动；Redis 不可用时直接读库并记录告警日志 |
| 一致性 | 先更新数据库，再删除缓存；删除失败时记日志，靠 TTL 兜底（步骤 4 起改为经 Outbox 事件重试删除） |

布隆过滤器：

- Redis 8 镜像自带 RedisBloom 模块，但当前 compose 自定义了启动命令，没有加载，需要加上 `--loadmodule`。
- 注册提交后执行 `BF.INSERT`（带容量和误判率参数，过滤器不存在时按这些参数创建）。
- 只在缓存未命中、回源之前查过滤器，命中缓存时不多一次 Redis 往返。
- 只有标记键 `account:user_bf:ready` 和过滤器都存在时，才信任“不存在”的判断。加入失败时后台重试；仍失败则写入脏标记、删除就绪标记并重建，重建完成后重新写入标记。这样过滤器漏加时不会把真实用户误判为不存在。完整机制见第 8 节。

跨实例的 Redis 互斥锁和逻辑过期不在本步骤使用：用户资料不是集中的热点键，等步骤 3 的群成员列表再引入。

### 3.6 事务与事件

| 操作 | 事务内 | 提交后 |
|---|---|---|
| 注册 | 插入用户（手机号唯一约束兜底并发注册）、插入会话 | `BF.ADD`，签发令牌 |
| 登录 | 吊销同一设备的旧会话、插入新会话、旧会话写 `SessionRevoked` 事件 | 旧会话写入吊销名单，签发令牌 |
| 退出、踢设备 | 吊销会话（`WHERE revoked_at IS NULL`，重复操作是幂等的）、写 `SessionRevoked` 事件 | 写入吊销名单 |
| 封禁 | 修改用户状态、吊销全部会话、写 `UserBanned` 事件 | 写入用户级吊销标记，删除资料缓存 |
| 修改资料 | 更新用户 | 删除资料缓存 |

- 事务内只操作数据库，Redis 和短信都在事务外。
- 提交后写吊销名单失败时记错误日志，此时访问令牌最多还能用 15 分钟，刷新令牌已经在数据库中失效。
- Outbox 事件写入 `outbox_events` 表，步骤 4 的发布器把它们投递到 Kafka，access 收到后断开对应连接。

### 3.7 配置与热更新

开发者要求限额类参数可配置、运行中可以调整（2026-11-18）。配置项分两类：

| 类别 | 内容 | 生效方式 |
|---|---|---|
| 策略 `policy` | 短信（有效期、验证次数、发送间隔、每号每日上限、每 IP 限流）、登录防护（失败次数、统计窗口、锁定时长、每 IP 限流） | **热更新**：修改配置文件后自动加载，不用重启 |
| 静态 | 令牌密钥与有效期、bcrypt cost、短信发送方、缓存 TTL、`trusted_proxies` | 重启后生效 |

- 热更新用 Kratos 配置的 `Watch`：文件变化后重新解析 `policy`，先用 protovalidate 校验（例如限额必须大于 0），通过后原子替换；不通过时保留旧值并记录错误日志。
- 令牌有效期不做热更新：缩短访问令牌有效期时，已签发的令牌仍按旧值有效，吊销记录的 TTL 必须覆盖它们。吊销记录使用单独的 `revocation_ttl`（默认 1 小时），启动时校验它不小于访问令牌有效期。
- 多实例部署（步骤 5）时，各实例挂载同一份配置文件；步骤 10 的可选扩展可以把策略改由 Consul KV 下发。
- 暂不做管理后台修改配置的接口，需要时再加。

### 3.8 ID

`pkg/idgen` 实现 snowflake：

- 41 位毫秒时间戳（纪元为 2026-01-01）、10 位节点号、12 位序列号。
- 节点号用 Redis 租约分配：`idgen:node:{n}` 用 `SET NX PX 30000` 抢占，值为实例 ID；每 10 秒用 Lua 校验持有者后续期。续期失败并超过租约期后停止发号，避免和接手这个节点号的实例重复。
- 时钟回拨不超过 5 毫秒时等待，超过时返回错误。
- 所有服务共用一个节点号空间（共 1024 个），不同实体的 ID 也不会相同。

## 4. 数据结构与接口

### 4.1 表（库 `marschat_account`）

时间列一律为 `DATETIME(3)`，按 UTC 读写。

**users**

| 列 | 类型 | 说明 |
|---|---|---|
| `id` | `BIGINT` PK | snowflake（有符号，与 Go `int64`、proto `int64` 对应） |
| `phone` | `VARCHAR(16)` | 唯一索引 `uk_phone` |
| `password_hash` | `VARCHAR(100)` | bcrypt 为 60 个字符，留出更换算法的余量 |
| `nickname` | `VARCHAR(32)` | |
| `avatar` | `VARCHAR(64)` | media 文件 ID，步骤 6 前为空 |
| `gender` | `TINYINT` | 0 未设置、1 男、2 女 |
| `birthday` | `DATE NULL` | |
| `signature` | `VARCHAR(100)` | |
| `role` | `TINYINT` | 1 普通用户、2 管理员 |
| `status` | `TINYINT` | 1 正常、2 封禁 |
| `created_at` / `updated_at` | | |

本步骤不做删除账号，所以没有软删除列，也就不存在软删除与唯一索引的冲突。

**sessions**（一台设备一次登录）

| 列 | 类型 | 说明 |
|---|---|---|
| `id` | `BIGINT` PK | snowflake，即令牌中的 `sid` |
| `user_id` | `BIGINT` | |
| `device_id` | `VARCHAR(64)` | 客户端生成并持久保存的设备标识 |
| `platform` | `TINYINT` | 1 Web、2 Android、3 iOS、4 桌面 |
| `device_name` | `VARCHAR(64)` | |
| `refresh_hash` / `prev_refresh_hash` | `BINARY(32)` | 当前与上一个刷新令牌的 SHA-256 |
| `rotated_at` | `DATETIME(3)` | 最近一次轮换时间 |
| `refresh_expires_at` | `DATETIME(3)` | |
| `login_ip` / `last_ip` | `VARCHAR(45)` | |
| `last_active_at` | `DATETIME(3)` | 刷新时更新 |
| `revoked_at` | `DATETIME(3) NULL` | |
| `revoke_reason` | `TINYINT` | 0 未吊销、1 退出、2 被踢、3 封禁、4 同设备重新登录、5 刷新令牌重放 |
| `active_device` | 生成列 `IF(revoked_at IS NULL, device_id, NULL)` | 唯一索引 `uk_user_active_device (user_id, active_device)` |

- `uk_user_active_device`：保证同一用户同一设备最多一个有效会话。已吊销的会话这一列为 NULL，不占用唯一约束，历史记录可以保留。这是处理“软删除与唯一索引冲突”的写法。
- `idx_user_revoked (user_id, revoked_at)`：设备列表查询 `WHERE user_id = ? AND revoked_at IS NULL`。
- 刷新时按主键查（会话 ID 在令牌里），不需要额外索引。

**outbox_events**（各服务同构，由 `pkg/outbox` 定义）

| 列 | 说明 |
|---|---|
| `id` | `BIGINT AUTO_INCREMENT` PK，发布顺序 |
| `topic` / `event_key` | Kafka topic 与分区键 |
| `event_type` / `payload` | 事件类型与 protobuf 编码的内容 |
| `created_at` / `published_at` | `published_at` 为 NULL 表示未发布，索引 `idx_unpublished (published_at, id)` |

### 4.2 接口

对外接口（`AccountService`，HTTP 与 gRPC）：

| 方法 | HTTP | 鉴权 | 说明 |
|---|---|---|---|
| `SendSmsCode` | `POST /v1/auth/sms-codes` | 否 | `phone`、`purpose`（注册或登录），返回冷却秒数 |
| `Register` | `POST /v1/auth/register` | 否 | `phone`、`sms_code`、`password`、`nickname`、`device`，返回令牌与资料 |
| `LoginByPassword` | `POST /v1/auth/login/password` | 否 | `phone`、`password`、`device` |
| `LoginBySms` | `POST /v1/auth/login/sms` | 否 | `phone`、`sms_code`、`device` |
| `RefreshToken` | `POST /v1/auth/refresh` | 否 | `refresh_token`，返回新的令牌对 |
| `Logout` | `POST /v1/auth/logout` | 是 | 吊销当前会话 |
| `GetMe` | `GET /v1/users/me` | 是 | 自己的完整资料（含手机号） |
| `UpdateMe` | `PATCH /v1/users/me` | 是 | 用 proto3 `optional` 字段表示只更新传了的字段 |
| `GetUser` | `GET /v1/users/{user_id}` | 是 | 公开资料（不含手机号） |
| `ListSessions` | `GET /v1/sessions` | 是 | 有效会话列表，标出当前设备 |
| `RevokeSession` | `DELETE /v1/sessions/{session_id}` | 是 | 踢掉自己的某个设备 |
| `BanUser` / `UnbanUser` | `POST /v1/admin/users/{user_id}/ban`、`/unban` | 管理员 | |

`device` 包含 `device_id`、`platform`、`device_name`。

内部接口（`AccountInternalService`，只有 gRPC，不经网关）：`BatchGetUsers(user_ids)` 返回公开资料和状态，供 social、message 使用，最多 100 个 ID，走资料缓存。

服务之间的 gRPC 调用暂不做身份认证（服务网络不对外暴露），mTLS 作为后续扩展。

### 4.3 错误码

`ErrorReason` 枚举，HTTP 状态码随错误类型确定：

| 原因 | HTTP |
|---|---|
| `SMS_SEND_TOO_FREQUENT`、`SMS_DAILY_LIMIT_EXCEEDED`、`RATE_LIMITED`、`LOGIN_LOCKED` | 429 |
| `SMS_CODE_INVALID`（错误或过期）、`SMS_CODE_ATTEMPTS_EXCEEDED`、`WEAK_PASSWORD` | 400 |
| `INVALID_CREDENTIALS`、`REFRESH_TOKEN_INVALID`；访问令牌缺失、无效、过期或已吊销时为 `UNAUTHORIZED`（与 Kratos JWT contrib 一致） | 401 |
| `USER_BANNED`、`PERMISSION_DENIED` | 403 |
| `USER_NOT_FOUND`、`SESSION_NOT_FOUND` | 404 |
| `PHONE_ALREADY_REGISTERED`、`REFRESH_TOKEN_ROTATED` | 409 |

参数格式错误由 protovalidate 返回 400，原因为 `VALIDATOR`。

### 4.4 配置新增

- `auth`（各服务共用）：公钥 `MARSCHAT_JWT_PUBLIC_KEY`、签发方、受众。
- `token`（仅 account）：私钥（Ed25519 种子的 base64，来自环境变量 `MARSCHAT_JWT_PRIVATE_KEY`）、`kid`、访问与刷新令牌有效期、轮换宽限期、`revocation_ttl`。
- `sms`：`provider`（本步骤只有 `log`）、固定验证码（仅 `log`）。
- `policy`：见 3.7，可热更新。
- `security`：bcrypt cost、`trusted_proxies`。只有请求来自这些地址时才读取 `X-Forwarded-For` 作为客户端 IP，默认为空；步骤 5 填入网关地址。

`make keys` 生成一对开发用密钥，写入被 Git 忽略的 `.env`。

### 4.5 第一个管理员

不提供设置管理员的接口。提供 `make grant-admin PHONE=...`，在 MySQL 容器里执行一条 `UPDATE`。

## 5. 候选方案与权衡

| 问题 | 采用 | 未采用及原因 |
|---|---|---|
| 令牌签名 | Ed25519 非对称 | HS256 共享密钥：每个服务都能签发令牌，泄露面大 |
| 令牌吊销 | 短期访问令牌 + 按会话、按用户的 Redis 名单 | 每个请求查数据库会话表：多一次数据库访问；只靠令牌过期：退出和封禁不能立即生效 |
| Redis 故障时的吊销检查 | **放行**（记告警日志），访问令牌最长 15 分钟，刷新仍查数据库（开发者已确认） | 拒绝：Redis 故障会让所有用户掉线 |
| 刷新令牌查找 | 令牌内带会话 ID，按主键查 | 按令牌哈希建唯一索引查：多一个索引，也无法区分“旧令牌重放”和“伪造令牌” |
| 同设备唯一会话 | 生成列加唯一索引 | 应用层先查后插：并发登录时会产生两个有效会话 |
| 布隆过滤器 | RedisBloom 模块 | 用 Redis bitmap 自己实现：多写代码，没有收益 |
| 迁移 | `migrate/migrate` 镜像，compose 中每个服务一个一次性任务 | 服务启动时自动迁移：多实例同时启动会竞争，生产上也不应由业务进程改表结构 |

## 6. 新增依赖与环境变化

| 依赖 | 用途 | 计划中是否已列出 |
|---|---|---|
| `kratos/contrib/middleware/jwt/v3`、`golang-jwt/jwt/v5` | JWT | 是（JWT 组件） |
| `kratos/contrib/middleware/validate/v3`、`buf.build/go/protovalidate` | 参数校验 | 是（protovalidate） |
| `golang.org/x/crypto` | bcrypt | 是（bcrypt） |
| `golang.org/x/sync` | singleflight | 已是间接依赖 |
| 阿里云短信 SDK | 短信 | 本步骤不引入，有 AccessKey 后再加 |
| `alicebob/miniredis/v2`（仅测试） | 不依赖真实 Redis 测试 Lua 脚本和限流 | 否，开发者已同意（2026-11-18） |
| buf 依赖 `buf.build/googleapis/googleapis`、`buf.build/bufbuild/protovalidate` | HTTP 注解与校验规则 | 是（生成 HTTP 代码、protovalidate） |
| `migrate/migrate` 镜像 | 执行迁移 | 是 |

环境变化：

- compose 的 Redis 增加 `--loadmodule` 加载 RedisBloom。
- 新增迁移任务，需要挂载 `app/account/migrations`。Docker Desktop 原来只共享了 `deploy/mysql/init`，改为共享项目根目录，之后的新挂载不会再弹窗。

## 7. 风险与验证

| 风险 | 应对 |
|---|---|
| Kratos JWT、validate contrib 是伪版本，可能有类似步骤 1 选择器那样的问题 | 已读过两者源码（见 3.2）；实施时先写最小的接入测试，再写业务代码 |
| 阿里云短信本步骤不实现 | 计划中保留待办，配置 AccessKey 后实现并验证 |
| 布隆过滤器与数据库不一致 | 使用 `ready` 标记，过滤器异常时退回查库，见 3.5 |

验证方案与结果（2026-11-18）：

- 单元测试（`go test -race ./...` 通过）：
  - biz 层用假 repo，覆盖注册、两种登录、验证码用途隔离与次数上限、发送失败回滚、登录锁定、同设备替换、刷新轮换与重放、踢设备、退出幂等、封禁与解封、生日校验。
  - 短信验证码与登录失败计数的 Lua 脚本、`pkg/ratelimit`、`pkg/idgen`、`pkg/cache` 用 miniredis 测试；`pkg/auth` 测试签发、校验、算法混淆和吊销；`pkg/middleware/accesslog` 测试日志不含请求体、内部错误脱敏和 panic 恢复。
  - 配置：解析真实配置文件；修改 `policy` 后热更新生效，非法值被拦下并保留旧值。
- e2e（`make e2e`，真实 MySQL、Redis、Consul，6 个用例通过）：发码与发送间隔、注册、资料修改与缓存失效、多设备登录、刷新轮换与宽限期、踢设备后访问令牌立即失效、退出、短信登录、旧刷新令牌超过宽限期重放导致会话吊销、封禁后访问令牌失效且无法登录和刷新、解封后可登录、公开资料不含手机号、内部 `BatchGetUsers`、参数校验与鉴权错误。
- `EXPLAIN` 结果记录在 [data-model.md](../architecture/data-model.md#2-表)。
- 布隆过滤器的重建与降级只在 e2e 中经过正常路径（启动重建、注册后加入、查询不存在的 ID），失败重试和脏标记分支没有自动化测试：miniredis 不支持 RedisBloom 命令。

## 8. 实施中的调整（2026-11-18）

实现时发现以下问题，按“先核实再修正”处理，均已在代码中落实：

| 问题 | 处理 |
|---|---|
| Kratos 自带的 `logging` 中间件把整个请求体写进日志，登录、注册请求里的密码、验证码、刷新令牌会落盘；`recovery` 记录 panic 时也打印请求体；非 Kratos 错误（SQL 错误等）会原样返回给客户端 | 新增 `pkg/middleware/accesslog` 替代两者：只记操作名、状态码、原因、耗时，5xx 记 ERROR；panic 恢复不打印请求体；内部错误记日志后换成不含细节的 500。见 [lessons/kratos-v3-server-defaults.md](../lessons/kratos-v3-server-defaults.md) |
| Kratos v3 的 `json` codec 改为纯标准库实现，HTTP 收发 proto 消息时枚举只能写数字、int64 不是字符串、Timestamp 编码成对象 | 新增 `pkg/jsoncodec` 覆盖注册 `json` codec，proto 消息按 protobuf JSON 规则编解码，见 [lessons/kratos-v3-server-defaults.md](../lessons/kratos-v3-server-defaults.md) |
| 登录与封禁并发时，登录可能在封禁提交之后插入一个有效会话并签发令牌 | 登录事务内先 `SELECT ... FOR UPDATE` 锁住用户行再确认状态，与封禁的 `UPDATE` 互斥；同一用户的并发登录因此串行，也避免了同一设备并发登录在唯一索引上死锁 |
| 封禁的用户级吊销时间如果取事务开始前的时间，并发登录签发的令牌 `iat` 可能晚于它 | 吊销时间取封禁事务提交之后的时间；重复封禁也重写一次，用于修复上次写入失败 |
| 布隆过滤器重建期间有注册加入失败，重建结果可能漏掉它 | 加入失败先后台重试 3 次；仍失败则写脏标记 `account:user_bf:dirty`，重建结束时发现脏标记就再重建一轮（最多 3 轮）；就绪标记带 24 小时 TTL，到期重建一次，兜底 Redis 故障期间没能记下的漏加 |
| 内存中的时间精确到纳秒，MySQL `DATETIME(3)` 对多余小数位四舍五入，写入后读回不相等 | 各用例的时钟截断到毫秒 |
| 唯一键冲突需要识别 MySQL 错误码 | 开启 GORM `TranslateError`，repo 只判断 `gorm.ErrDuplicatedKey` |
| 每日上限的剩余时间如果取计数键的 TTL，并不是“到次日零点” | 计数键 TTL 固定 25 小时只用于清理（键名带日期，自然换天），剩余时间由 biz 按北京时间计算到次日零点 |
