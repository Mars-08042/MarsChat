# 数据模型

> 本文记录**新后端**的库、表、索引和 Redis 键，随重构步骤更新，最近一次更新于步骤 3（2026-11-19）。设计原则见 [后端重构计划](../plans/active/backend-refactor.md#数据库与缓存设计原则)。旧基线的表结构放在文末附录，只作功能参考。

## 1. 数据库与账号

每个服务一个库、一个账号，账号只有本库的权限，服务之间不能跨库读写，跨服务数据通过接口或事件获取。

| 服务 | 库 | 账号 | 密码环境变量 | 引入步骤 |
|---|---|---|---|---|
| account | `marschat_account` | `account` | `MARSCHAT_ACCOUNT_DB_PASSWORD` | 1 |
| social | `marschat_social` | `social` | `MARSCHAT_SOCIAL_DB_PASSWORD` | 1 |
| message | `marschat_message` | `message` | `MARSCHAT_MESSAGE_DB_PASSWORD` | 1（建库），4（建表） |
| media | `marschat_media` | `media` | `MARSCHAT_MEDIA_DB_PASSWORD` | 1（建库），6（建表） |

access 和 delivery 不持有数据库，状态放在 Redis。

- 库和账号由 [deploy/mysql/init/01-create-databases.sh](../../deploy/mysql/init/01-create-databases.sh) 创建，只在 MySQL 数据卷首次初始化时执行。新增服务库时修改该脚本，已有数据卷需要手动建库或重建数据卷。
- 字符集 `utf8mb4`，排序规则 `utf8mb4_0900_ai_ci`。
- 连接参数 `loc=UTC&parseTime=true`：时间列按 UTC 读写，展示时再转换时区。

## 2. 表

表结构用版本化 SQL 迁移创建，迁移文件在 `app/<服务>/migrations/`，由 `make migrate-<服务>` 执行（见 [deployment.md](../deployment.md#数据库迁移)），不使用 AutoMigrate。

通用约定：

- ID 为 snowflake（`pkg/idgen`），列类型 `BIGINT`（有符号，对应 Go 与 proto 的 `int64`），JSON 中按字符串输出。
- 时间列为 `DATETIME(3)`，按 UTC 读写；应用写入前把时间截断到毫秒，与列精度一致。
- 状态类列直接存 proto 枚举的数值，0 保留给 `UNSPECIFIED`。

### 2.1 account（`marschat_account`，迁移 `000001_init`）

**users**

| 列 | 类型 | 说明 |
|---|---|---|
| `id` | `BIGINT` PK | snowflake |
| `phone` | `VARCHAR(16)` | 唯一索引 `uk_phone` |
| `password_hash` | `VARCHAR(100)` | bcrypt（cost 12） |
| `nickname` | `VARCHAR(32)` | 接口限制 1～16 个字符 |
| `avatar` | `VARCHAR(64)` | media 文件 ID，步骤 6 前为空 |
| `gender` | `TINYINT` | 0 未设置、1 男、2 女 |
| `birthday` | `DATE NULL` | |
| `signature` | `VARCHAR(100)` | |
| `role` | `TINYINT` | 1 普通用户、2 管理员 |
| `status` | `TINYINT` | 1 正常、2 封禁 |
| `created_at` / `updated_at` | `DATETIME(3)` | |

**sessions**（一台设备的一次登录）

| 列 | 类型 | 说明 |
|---|---|---|
| `id` | `BIGINT` PK | snowflake，即访问令牌中的 `sid`、刷新令牌的前缀 |
| `user_id` | `BIGINT` | |
| `device_id` / `platform` / `device_name` | `VARCHAR(64)` / `TINYINT` / `VARCHAR(64)` | 平台 1 Web、2 Android、3 iOS、4 桌面 |
| `refresh_hash` / `prev_refresh_hash` | `BINARY(32)` / `BINARY(32) NULL` | 当前与上一个刷新令牌随机部分的 SHA-256 |
| `rotated_at` | `DATETIME(3) NULL` | 最近一次轮换时间，判断重放宽限期 |
| `refresh_expires_at` | `DATETIME(3)` | 每次刷新顺延 30 天 |
| `login_ip` / `last_ip` | `VARCHAR(45)` | |
| `last_active_at` | `DATETIME(3)` | 刷新时更新 |
| `revoked_at` / `revoke_reason` | `DATETIME(3) NULL` / `TINYINT` | 原因 0 未吊销、1 退出、2 被踢、3 封禁、4 同设备重新登录、5 刷新令牌重放 |
| `active_device` | 虚拟生成列 `IF(revoked_at IS NULL, device_id, NULL)` | 只用于唯一索引 |

**outbox_events**：结构见 [pkg/outbox](../../pkg/outbox/outbox.go)，各服务同构。account 写入的事件为 `SessionRevoked`、`UserBanned`（topic `marschat.account.events`，分区键为用户 ID），发布器在步骤 4 实现。

索引与查询（`EXPLAIN` 于 2026-11-18 在 MySQL 9.7.4 上核对）：

| 索引 | 查询 | 说明 |
|---|---|---|
| `users.uk_phone` | 登录、发码前判断是否注册：`WHERE phone = ?` | 常量查找；同时兜底并发注册（唯一键冲突转成 `PHONE_ALREADY_REGISTERED`） |
| `users.PRIMARY` | 资料回源、批量资料 `WHERE id IN (...)`、登录与封禁的行锁、布隆过滤器重建 `WHERE id > ? ORDER BY id LIMIT 1000` | 重建扫描为覆盖索引范围扫描 |
| `sessions.uk_user_active_device (user_id, active_device)` | 同设备登录时吊销旧会话：`WHERE user_id = ? AND active_device = ? FOR UPDATE` | 已吊销会话这一列为 NULL，不占唯一约束，所以同一设备最多一个有效会话，历史全部保留 |
| `sessions.idx_user_revoked (user_id, revoked_at)` | 设备列表、封禁时吊销全部会话：`WHERE user_id = ? AND revoked_at IS NULL` | 数据量很小时优化器可能改用 `uk_user_active_device` 的 `user_id` 前缀，两者都在候选索引中 |
| `sessions.PRIMARY` | 刷新：按令牌里的会话 ID 查找；轮换 `UPDATE ... WHERE id = ? AND refresh_hash = ? AND revoked_at IS NULL` | 条件更新保证并发刷新只有一个成功 |
| `outbox_events.idx_unpublished (published_at, id)` | 发布器取未发布事件 `WHERE published_at IS NULL ORDER BY id` | 步骤 4 使用 |

事务边界见 [account 设计 3.6](../design/account-service.md#36-事务与事件)。登录事务先 `SELECT ... FOR UPDATE` 锁住用户行，与封禁互斥。

### 2.2 social（`marschat_social`，迁移 `000001_init`）

**friendships**：一段好友关系存两行（A→B 与 B→A），在同一事务中插入（`INSERT IGNORE`）和删除。

| 列 | 类型 | 说明 |
|---|---|---|
| `user_id` / `friend_id` | `BIGINT` | 主键 `(user_id, friend_id)` |
| `created_at` | `DATETIME(3)` | 成为好友的时间 |

**friend_requests**：保留历史，被拒绝后再申请生成新记录。

| 列 | 类型 | 说明 |
|---|---|---|
| `id` | `BIGINT` PK | snowflake，也是列表游标 |
| `from_user_id` / `to_user_id` | `BIGINT` | |
| `message` | `VARCHAR(100)` | 附言，重复申请时覆盖 |
| `status` | `TINYINT` | 1 待处理、2 已同意、3 已拒绝、4 已失效（拉黑） |
| `created_at` / `updated_at` / `handled_at` | `DATETIME(3)`（`handled_at` 可空） | |
| `pending_flag` | 虚拟生成列 `IF(status = 1, 1, NULL)` | 只用于唯一索引 `uk_pending` |

**blocks**：单向拉黑，主键 `(user_id, blocked_id)`，另有 `created_at`。不影响好友关系。

**chat_groups**（`GROUPS` 是 MySQL 8 的保留字）

| 列 | 类型 | 说明 |
|---|---|---|
| `id` | `BIGINT` PK | snowflake，与用户 ID 共用节点号空间 |
| `name` / `avatar` / `notice` | `VARCHAR(32)` / `VARCHAR(64)` / `VARCHAR(500)` | 名称接口限制 1～30 个字符，去掉首尾空白后不能为空 |
| `owner_id` | `BIGINT` | 冗余，转让群主时修改 |
| `join_mode` | `TINYINT` | 1 直接加入、2 需审核 |
| `mute_all` | `TINYINT` | 全员禁言 |
| `member_count` | `INT` | 在锁住群行的事务内维护，与成员行数一致 |
| `status` | `TINYINT` | 1 正常、2 已解散 |
| `version` | `INT` | 资料乐观锁；修改资料、转让群主、解散时加一 |
| `created_at` / `updated_at` / `dismissed_at` | `DATETIME(3)` | `updated_at` 只随资料变化，成员进出不更新 |

**group_members**：退群、被移除直接删除行，历史由事件记录。

| 列 | 类型 | 说明 |
|---|---|---|
| `group_id` / `user_id` | `BIGINT` | 主键 `(group_id, user_id)` |
| `role` | `TINYINT` | 1 成员、2 管理员、3 群主 |
| `muted_until` | `DATETIME(3) NULL` | 单人禁言截止时间，过期自动失效 |
| `inviter_id` | `BIGINT` | 邀请人，自己加入或审核通过为 0 |
| `joined_at` | `DATETIME(3)` | |

**group_join_requests**：结构同 `friend_requests`，用 `group_id`、`user_id` 代替两个用户 ID，另有 `handled_by`（处理人，自动失效为 0）。状态 4 已失效用于被邀请入群、群解散。

**outbox_events**：同构。social 写入的事件见 [events.proto](../../api/social/v1/events.proto)（topic `marschat.social.events`；群事件按群 ID、好友事件按两人中较小的 ID、申请事件按接收方 ID 分区）。

索引与查询（`EXPLAIN` 于 2026-11-19 在 MySQL 9.7.4 上核对，临时库中每张表造了 1 万～5 万行数据并 `ANALYZE`）：

| 索引 | 查询 | 说明 |
|---|---|---|
| `friendships.PRIMARY` | 是否好友 `WHERE user_id = ? AND friend_id = ?`；好友列表 `WHERE user_id = ? AND friend_id > ? ORDER BY friend_id LIMIT n`；邀请时过滤好友 `friend_id IN (...)` | 常量查找 / 主键范围扫描 |
| `blocks.PRIMARY` | 双向拉黑检查 `WHERE (user_id, blocked_id) IN ((a, b), (b, a))` | 覆盖索引上的两个点范围 |
| `friend_requests.uk_pending (from_user_id, to_user_id, pending_flag)` | 查、改某一对用户的待处理申请 `... AND pending_flag = 1` | 待处理之外的记录这一列为 NULL，不占唯一约束；同时兜底并发申请（冲突时整个事务重试一次） |
| `friend_requests.idx_to (to_user_id, status, id)` | 收到的待处理申请 `WHERE to_user_id = ? AND status = 1 AND id < ? ORDER BY id DESC` | 倒序范围扫描，无排序 |
| `friend_requests.idx_from (from_user_id, id)` | 发出的申请 `WHERE from_user_id = ? [AND id < ?] ORDER BY id DESC` | 某个用户的申请只有几十条时，优化器可能改用 `uk_pending` 的 `from_user_id` 前缀加排序；单个用户 5000 条时走 `idx_from` 倒序扫描，只读 21 行 |
| `chat_groups.PRIMARY` | 成员变化前锁群行 `WHERE id = ? FOR UPDATE`；布隆过滤器重建扫描 | |
| `group_members.PRIMARY` | 成员列表、成员列表缓存回源 `WHERE group_id = ? [AND user_id > ?] ORDER BY user_id`；操作者与对象的成员行 | 主键前缀，无排序 |
| `group_members.idx_user (user_id, group_id)` | 我的群 `WHERE user_id = ? AND group_id > ? ORDER BY group_id`；`FilterMembers` 的 `group_id = ? AND user_id IN (...)` 也会选它 | |
| `group_join_requests.idx_group (group_id, status, id)` | 群的待处理申请（倒序）；解散时 `UPDATE ... WHERE group_id = ? AND status = 1` | |
| `group_join_requests.uk_pending (group_id, user_id, pending_flag)` | 查某人对某群的待处理申请；邀请时让被邀请人的申请失效 | |

单聊发言权限是 2 次主键查询（拉黑、好友），群聊发言权限读群资料缓存和成员列表缓存。事务边界与并发控制见 [social 设计 3.4、3.9](../design/social-service.md#34-群)。

## 3. Redis 键

所有键带服务前缀。限额类数值来自可热更新的 `policy` 配置，表中为默认值。

| 键 | 类型 | TTL | 写入 / 失效 | 用途 |
|---|---|---|---|---|
| `auth:revoked:sid:{sid}` | String | `token.revocation_ttl`（1 小时，不小于访问令牌有效期） | 退出、踢设备、同设备重新登录、刷新令牌重放时写入 | 吊销名单（会话级），所有服务的鉴权中间件读取 |
| `auth:revoked:user:{uid}` | String，值为封禁时间（Unix 秒） | 同上 | 封禁事务提交后写入；解封不删除 | 吊销名单（用户级），拒绝 `iat` 不晚于该时间的令牌 |
| `account:user:{id}` | String（1 字节标记 + JSON） | 30 分钟 ±10% 抖动；空值标记 1 分钟 | 资料修改、封禁、解封、设置管理员后删除 | 用户资料缓存（Cache-Aside，不含密码哈希） |
| `account:user_bf` | RedisBloom | 无 | 注册后 `BF.INSERT`；重建时整体替换 | 用户 ID 布隆过滤器，容量 100 万、误判率 0.1% |
| `account:user_bf:ready` | String | 24 小时 | 重建完成后写入；加入失败时删除 | 存在时才信任过滤器的“不存在”判断，到期触发一次重建 |
| `account:user_bf:dirty` | String | 10 分钟 | 加入过滤器重试仍失败时写入 | 让进行中的重建再做一轮 |
| `account:user_bf:rebuild` | String，值为持有者随机串 | 5 分钟 | 重建开始时 `SET NX`，结束时校验持有者后删除 | 多实例只有一个在重建 |
| `account:sms:code:{purpose}:{phone}` | Hash：`h` 验证码 SHA-256、`n` 错误次数 | 5 分钟 | 发送时覆盖；验证成功或错误 5 次后删除 | 短信验证码，注册（1）与登录（2）分开 |
| `account:sms:cooldown:{phone}` | String | 60 秒 | 发送时设置；短信平台失败时删除 | 发送间隔 |
| `account:sms:daily:{phone}:{yyyymmdd}` | String 计数 | 25 小时（只用于清理） | 发送时加一；短信平台失败时减一 | 每号每日上限 10 条，日期按北京时间 |
| `account:rl:sms_ip:{ip}` | ZSET | 窗口长度 | `pkg/ratelimit` 滑动窗口 | 每 IP 每小时 20 次发码；Redis 故障时拒绝发码 |
| `account:rl:login_ip:{ip}` | ZSET | 窗口长度 | 同上 | 每 IP 每分钟 20 次登录；Redis 故障时放行 |
| `account:login_fail:{phone}` | String 计数 | 首次失败起 15 分钟；达到 5 次时改为锁定 15 分钟 | 密码错误时加一；登录成功后删除 | 密码错误锁定 |
| `social:group:{id}` | String（1 字节标记 + JSON） | 30 分钟 ±10%；空值标记 1 分钟 | 资料修改、成员数变化、转让、解散后删除 | 群资料缓存，未命中时先查群 ID 布隆过滤器 |
| `social:group:members:{id}` | String（1 字节标记 + JSON 数组：用户 ID、角色、禁言截止） | 30 分钟 ±10% | 任何成员、角色、禁言变化后删除 | 全部成员，供发言权限校验和投递；开启跨实例重建锁 |
| `social:group:members:{id}:lock` | String，值为持有者随机串 | 3 秒 | 缓存未命中时 `SET NX PX`，回填后校验持有者再删除 | 重建锁：没抢到的实例每 50 毫秒重读缓存，最多 200 毫秒，之后直接回源 |
| `social:group_bf`、`:ready`、`:dirty`、`:rebuild` | 同 `account:user_bf*` | 同上 | 建群后 `BF.INSERT` | 群 ID 布隆过滤器（`pkg/bloom`），含已解散的群 |
| `social:rl:friend_request:{uid}` | ZSET | 窗口长度 | `pkg/ratelimit` | 每人每小时 20 条好友申请 |
| `social:rl:group_create:{uid}` | ZSET | 窗口长度 | 同上 | 每人每天（滑动 24 小时）建 10 个群 |
| `social:rl:join_request:{uid}` | ZSET | 窗口长度 | 同上 | 每人每小时 20 次入群（直接加入也计入）；social 的限流在 Redis 故障时都放行 |
| `idgen:node:{n}` | String，值为实例 ID | 30 秒，每 10 秒续期 | 启动时 `SET NX` 抢占节点号 | snowflake 节点号租约（所有服务共用 0～1023） |

## 附录：旧基线表结构（已移除）

旧单体使用单库 `marschat`，6 张表由 GORM `AutoMigrate` 创建，代码已在步骤 0 删除（最后版本见提交 `6624cea` 中的 `internal/model/`）。下表只用于对照功能，新设计不沿用这些结构。

| 表 | 关键字段 | 说明 |
|---|---|---|
| `user_info` | `uuid`、`telephone`、`password`（明文）、`nickname`、`avatar`、`gender`、`birthday`、`signature`、`is_admin`、`status`、`deleted_at` | 手机号是登录名 |
| `group_info` | `uuid`、`name`、`notice`、`members`（JSON 数组）、`member_cnt`、`owner_id`、`add_mode`、`avatar`、`status`、`deleted_at` | 成员存在 JSON 列里 |
| `user_contact` | `user_id`、`contact_id`、`contact_type`、`status`、`deleted_at` | 好友关系双向各一条；群关系只有“用户→群”一条 |
| `contact_apply` | `uuid`、`user_id`、`contact_id`、`contact_type`、`status`、`message`、`last_apply_at` | 同一对（申请人，目标）复用一条记录 |
| `session` | `uuid`、`send_id`、`receive_id`、`receive_name`、`avatar`、`deleted_at` | 会话按所有者单向存储 |
| `message` | `uuid`、`session_id`、`type`、`content`、`url`、`send_id`、`receive_id`、`file_*`、`status`、`av_data`、`created_at` | 单聊按（send_id，receive_id）双向查询 |

### 旧基线枚举

| 枚举 | 取值 |
|---|---|
| 用户状态 `user_info.status` | 0 正常、1 禁用 |
| 群状态 `group_info.status` | 0 正常、1 禁用、2 解散（未使用） |
| 加群方式 `group_info.add_mode` | 0 直接加入、1 需审核 |
| 联系类型 `contact_type` | 0 用户、1 群聊 |
| 联系状态 `user_contact.status` | 0 正常、1 被拉黑、2 拉黑、3 被删除、4 删除、5 被禁言（未使用）、6 退出群聊、7 被踢出群聊（未使用） |
| 申请状态 `contact_apply.status` | 0 申请中、1 通过、2 拒绝、3 拉黑 |
| 消息类型 `message.type` | 0 文本、1 语音（未实现）、2 文件、3 音视频通话信令 |
| 消息状态 `message.status` | 0 未发送、1 已发送 |

旧基线的业务规则见 `docs/product/modules/` 下的模块文档，旧的 Redis 键设计见提交 `6624cea` 中本文的第 4 节。
