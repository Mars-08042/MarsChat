# 设计：social 服务（重构步骤 3）

- 状态：已采纳（2026-11-19 开发者确认） | 日期：2026-11-19 | 关联：[重构计划步骤 3](../plans/active/backend-refactor.md#3-social-服务)、[ADR-0005](../architecture/adr/0005-distributed-architecture.md)、[ADR-0006](../architecture/adr/0006-message-sync-model.md)、[account 设计](account-service.md)、旧基线 [contact.md](../product/modules/contact.md)、[group.md](../product/modules/group.md)

## 1. 背景与目标

旧基线的好友与群：

- 审批接口不校验操作者（TD-01）；
- 群成员存在 JSON 列里，计数会算错，解散不改状态（TD-16）；
- 重复申请的状态处理错误（TD-17）；
- 多处缓存失效无效（TD-09）。

步骤 3 实现 social 服务，并为步骤 4 的消息链路提供发言权限和群成员查询。

目标：

- 好友：申请、同意、拒绝、删除；拉黑与解除拉黑。
- 群：创建、直接加入或审核加入、邀请好友入群、成员与角色（群主、管理员、成员）、转让群主、移除成员、退出、解散、禁言（单人与全员）。
- 内部 gRPC：发言权限校验（给 message）、群成员列表（给 delivery，走缓存）、批量群资料（给 message 的会话列表）。
- 群资料修改用乐观锁；好友与群的变化写 Outbox 事件，由 message 创建或更新会话、由 delivery 通知在线客户端（步骤 4 接入）。
- 第一次真正的服务间调用：social 经 Consul 服务发现调用 account。

不在本步骤：

- 好友备注与分组、按手机号搜索用户、群邀请链接、群公告历史、@ 提醒；
- 系统管理员禁用群（只提供解散任意群）；
- 熔断与重试（步骤 5）。

## 2. 需求与约束

- 用户按对方的用户 ID 申请好友、按群 ID 申请入群，与旧基线一致。
- 单聊要求双方是好友且没有任何一方拉黑另一方；群聊要求是群成员且未被禁言。
- 群规模中小（默认上限 500 人，可热更新），不支持超大群（ADR-0005）。
- 所有对外接口都需要登录；当前用户来自访问令牌，不信任请求里的用户 ID（修正 TD-01）。
- social 不存用户资料，资料以 account 为准。

## 3. 方案

### 3.1 模块划分

| 位置 | 内容 |
|---|---|
| `api/social/v1/` | `social.proto`（对外接口）、`internal.proto`（内部 gRPC）、`error_reason.proto`、`events.proto` |
| `app/social/internal/` | `biz`（好友、拉黑、群、权限）、`data`（repo、缓存、account 客户端）、`service`、`server` |
| `app/social/migrations/` | 版本化 SQL |
| `pkg/bloom`（新增） | 从 account 的 `user_bloom.go` 抽出的通用 ID 布隆过滤器，account 与 social 共用 |
| `pkg/cache`（扩展） | 增加可选的跨实例重建锁，用于群成员列表 |
| `pkg/bootstrap`（扩展） | 把 account `server.go` 里组装中间件链的代码抽成 `ServerMiddlewares`，social 复用（实施时由 `pkg/middleware` 改到这里，见第 9 节） |

### 3.2 好友

**关系存储**：一段好友关系存两行（`friendships(user_id, friend_id)`，A→B 与 B→A），在同一事务中插入、删除。“我的好友”是一次主键前缀扫描，以后加备注也有地方放。

**申请规则**：

| 情况 | 结果 |
|---|---|
| 申请自己 | `400 CANNOT_TARGET_SELF` |
| 对方不存在或已封禁（调用 account 确认） | `404 USER_NOT_FOUND` |
| 已经是好友 | `409 ALREADY_FRIENDS`（修正 TD-17：已通过的申请不会被改回“申请中”） |
| 任何一方拉黑了另一方 | `403 FRIEND_REQUEST_NOT_ALLOWED`，不区分是谁拉黑了谁 |
| 已有我发给对方的待处理申请 | 更新附言和时间，不新增记录 |
| 对方已经向我发过待处理申请 | **直接成为好友**，对方的申请标记为已同意 |
| 其他 | 新建一条待处理申请 |

- 申请记录保留历史：被拒绝后可以再次申请，生成新记录（修正 TD-17：被拒后再申请对方能看到）。
- “同一对用户最多一条待处理申请”用生成列加唯一索引保证：`pending_flag = IF(status = 1, 1, NULL)`，唯一索引 `(from_user_id, to_user_id, pending_flag)`，与 account 的同设备唯一会话同一写法。
- 每人每小时最多发 20 条申请（`policy`，可热更新），防骚扰。

**处理申请**：只有接收方能同意或拒绝（修正 TD-01）。

- 用条件更新 `UPDATE ... WHERE id = ? AND to_user_id = ? AND status = 1` 保证并发同意只成功一次；影响行数为 0 时，查记录区分“不存在”和“已处理”。
- 同意时插入两行好友关系，用 `INSERT IGNORE` 保证幂等：两人互发申请并发处理时，两次同意都成功。

**删除好友**：删除两行关系，删除不存在的好友视为成功（幂等）。单聊会话由 message 保留，但之后无法再发消息。

### 3.3 拉黑

- 拉黑是独立的单向关系 `blocks(user_id, blocked_id)`，**不删除好友关系**（参考 teamgram 的 `user_peer_blocks`）。解除拉黑后恢复单聊，不需要重新加好友。
- 效果：任何一方拉黑另一方时，双方都不能单聊、不能互发好友申请；拉黑时两人之间的待处理申请标记为已失效。
- 被拉黑的一方不会收到通知；发消息时得到的错误与“对方不是好友”不同（`BLOCKED`），这一点与常见 IM 一致，见第 8 节待确认问题。
- 可以拉黑非好友（例如拉黑骚扰的申请人，替代旧基线的“拉黑申请”）。

### 3.4 群

**群资料**：表名为 `chat_groups`（`GROUPS` 是 MySQL 8 的保留字）。

- 字段：名称、头像、公告、群主、加群方式（1 直接加入、2 需审核）、全员禁言、成员数、状态（1 正常、2 已解散）、版本号。
- 修改资料用**乐观锁**：请求带上客户端读到的 `version`，`UPDATE ... WHERE id = ? AND version = ?`，影响行数为 0 时返回 `409 GROUP_VERSION_CONFLICT`，客户端重新读取后再改。两个管理员同时编辑时，后提交的一方不会静默覆盖前者。

**成员**：`group_members(group_id, user_id)` 为主键，群 ID 开头符合按会话分片的原则；另建 `(user_id, group_id)` 索引查“我的群”。

- 角色：3 群主、2 管理员、1 成员。
- 单人禁言用 `muted_until` 列，过期自动失效，不需要定时任务。
- 退群、被移除直接删除成员行，历史由事件记录，避免软删除与主键冲突。

**成员变化的并发控制**：入群、邀请、移除、退群、转让、解散都在事务内先 `SELECT ... FOR UPDATE` 锁住群行，再读写成员并更新 `member_count`。

- 同一个群的成员变化因此串行，`member_count` 与实际行数一致（修正 TD-16），满员检查不会被并发绕过。
- 群规模中小，串行化不是瓶颈。
- 资料修改不加行锁，用乐观锁：资料编辑跨越用户思考时间，不能长时间持锁。

**权限矩阵**（服务端在事务内重新读取操作者的成员行，修正 TD-01、TD-16）：

| 操作 | 群主 | 管理员 | 成员 |
|---|---|---|---|
| 修改资料、全员禁言 | 是 | 是 | 否 |
| 审批入群申请、邀请自己的好友入群 | 是 | 是 | 否 |
| 移除成员、单人禁言 | 是（管理员与成员） | 是（仅成员） | 否 |
| 设置或取消管理员、转让群主、解散 | 是 | 否 | 否 |
| 退群 | 先转让群主 | 是 | 是 |

系统管理员（令牌角色为管理员）可以解散任意群。

**入群**：

- 直接加入的群：申请即入群。
- 需审核的群：生成入群申请，管理员同意或拒绝。规则与好友申请相同：同一用户对同一群最多一条待处理申请，被拒后可再申请（修正 TD-17）。
- 已是成员：`409 ALREADY_GROUP_MEMBER`。
- 满员：`409 GROUP_FULL`。
- 管理员邀请：只能邀请自己的好友，跳过审核；已是成员的人跳过；被邀请人的待处理申请标记为已失效。
- 建群时可以带上初始成员（创建者的好友，最多 50 人）。

**解散**：

- 状态改为已解散（修正 TD-16）。
- 删除全部成员行；待处理的入群申请标记为已失效。
- 写 `GroupDismissed` 事件，事件里带解散前的成员 ID，供 message 和 delivery 使用。
- 解散后的群资料仍可查询（状态为已解散），客户端据此把会话显示为“群已解散”。

### 3.5 内部接口

`SocialInternalService`，只有 gRPC，不经网关：

| 方法 | 调用方 | 行为 |
|---|---|---|
| `CheckSendPermission(sender_id, peer_type, peer_id)` | message，每条消息一次 | 单聊：是好友且双方都没有拉黑（3 次主键查询）。群聊：群正常、发送者是成员、未被单人禁言、全员禁言时只有群主和管理员能发言（读群资料缓存和成员列表缓存）。不允许时返回带原因的错误，message 原样转给客户端 |
| `ListAllGroupMembers(group_id)` | delivery，每条群消息一次 | 返回全部成员 ID、角色和禁言截止时间，走成员列表缓存 |
| `BatchGetGroups(group_ids)` | message 的会话列表 | 群名称、头像、状态，走群资料缓存，最多 100 个 |

### 3.6 缓存

| 数据 | 键 | 策略 |
|---|---|---|
| 群资料 | `social:group:{id}` | `pkg/cache`，TTL 30 分钟 ±10%，空值 1 分钟；群 ID 布隆过滤器 `social:group_bf` 拦截不存在的 ID |
| 群成员列表 | `social:group:members:{id}` | `pkg/cache`，值为全部成员（ID、角色、禁言截止），TTL 30 分钟 ±10%；任何成员、角色、禁言变化后删除；**开启跨实例重建锁** |

- 成员列表是热点：每条群消息都要读一次（权限校验加投递）。它失效后，多个 message、delivery 实例会同时回源。进程内 singleflight 只能合并本实例的请求，所以给 `pkg/cache` 增加可选的重建锁：
  - 未命中时先 `SET NX PX 3000` 抢 `{key}:lock`；抢到的实例回源并回填，再校验持有者后释放锁。
  - 没抢到的实例每 50 毫秒重读一次缓存，最多等 200 毫秒；仍未命中就直接回源，宁可多一次查询，也不让请求失败。
  - 账号资料不是集中热点，不开启。
- 逻辑过期不用：群成员列表允许短暂删除后回源，不需要“永不过期 + 后台刷新”的复杂度。
- 好友关系、拉黑不缓存：都是主键查询，单聊权限校验 3 次主键查询在 1 毫秒以内（实施时用 `EXPLAIN` 确认）。
- 删除缓存失败时记 WARN，靠 TTL 兜底，步骤 4 改为经事件重试（同 TD-25）。

### 3.7 布隆过滤器抽到 `pkg/bloom`

account 的 `user_bloom.go` 已经实现了就绪标记、脏标记、分布式重建锁和失败重试，与业务无关。

- 抽成 `pkg/bloom`：参数为键名前缀、容量、误判率，以及一个“按 ID 分页扫描全表”的回调。
- account 改为调用它，键名不变（`account:user_bf*`），行为不变。
- social 用它建群 ID 过滤器 `social:group_bf*`。
- 抽取后补上 TD-27 中缺少的失败分支测试：用真实 Redis 的集成测试，构建标签 `e2e`。

### 3.8 调用 account

social 需要 account 提供：申请好友时确认对方存在且未封禁；好友列表、群成员列表、申请列表补上昵称和头像。

- 用 Kratos gRPC 客户端，地址 `discovery:///marschat-account`，经 Consul 发现，超时 1 秒。客户端中间件只有超时，重试与熔断在步骤 5 加入。
- 只调用 `AccountInternalService.BatchGetUsers`，列表接口每页最多 100 条，正好一次批量调用。
- 故障处理分两类：
  - **写操作需要的校验**（申请好友时确认对方存在）：account 不可用时返回 `503 DEPENDENCY_UNAVAILABLE`，不在无法确认时放行。
  - **列表里的资料补全**：account 不可用时仍返回列表，资料字段为空，记 WARN。客户端显示占位，好友和群成员列表不因 account 故障而完全不可用。

### 3.9 事务与事件

topic `marschat.social.events`。事件只写在事务内的 Outbox 表，发布器在步骤 4 实现。

| 事件 | 触发 | 分区键 | 消费方（步骤 4 起） |
|---|---|---|---|
| `FriendRequestCreated` | 新建好友申请 | 接收方 ID | delivery：通知接收方 |
| `FriendAdded` | 成为好友（同意或互相申请） | 两人中较小的 ID | message：准备单聊会话；delivery：通知双方 |
| `FriendDeleted` | 删除好友 | 两人中较小的 ID | delivery：通知双方 |
| `GroupCreated` | 建群 | 群 ID | message：创建群会话 |
| `GroupMembersJoined` | 加入、审核通过、邀请 | 群 ID | message：加入会话；delivery：通知 |
| `GroupMemberLeft` | 退群、被移除（带原因） | 群 ID | message：移出会话 |
| `GroupMemberUpdated` | 角色、禁言变化 | 群 ID | delivery：通知 |
| `GroupUpdated` | 资料修改（带新版本号） | 群 ID | message：刷新会话名称与头像缓存 |
| `GroupDismissed` | 解散（带解散前成员） | 群 ID | message、delivery |
| `GroupJoinRequestCreated` | 需审核的群收到新的入群申请 | 群 ID | delivery：通知群主和管理员 |

- 群事件按群 ID 分区，同一个群的“加入、退出”在同一分区内有序；好友事件按两人中较小的 ID 分区，同一对用户的事件有序。
- 拉黑不发事件：被拉黑方不应收到通知。拉黑方的其他设备在下次刷新列表时同步。
- 事务边界与 account 一致：事务内只操作数据库；缓存删除、调用 account 都在事务外。申请好友时先调用 account 确认对方存在，再开事务。

### 3.10 配置与热更新

与 account 相同的方式：`policy` 段热更新，其余重启生效。

| 配置段 | 内容 |
|---|---|
| `auth` | 公钥、签发方、受众（各服务相同） |
| `account` | account 服务地址（默认 `discovery:///marschat-account`）、调用超时 |
| `cache` | 群资料、成员列表 TTL，空值 TTL |
| `policy`（热更新） | 每人每小时好友申请上限 20、每人每天建群上限 10、每人每小时入群申请上限 20、群成员上限 500、建群时初始成员上限 50 |

调低群成员上限时不移除已有成员，只影响之后的加入。

## 4. 数据结构与接口

### 4.1 表（库 `marschat_social`）

时间列为 `DATETIME(3)`，按 UTC 读写；ID 为 snowflake，有符号 `BIGINT`；状态列存 proto 枚举数值。

**friendships**

| 列 | 类型 | 说明 |
|---|---|---|
| `user_id` / `friend_id` | `BIGINT` | 主键 `(user_id, friend_id)`，一段关系两行 |
| `created_at` | `DATETIME(3)` | |

**friend_requests**

| 列 | 类型 | 说明 |
|---|---|---|
| `id` | `BIGINT` PK | snowflake，也是列表游标 |
| `from_user_id` / `to_user_id` | `BIGINT` | |
| `message` | `VARCHAR(100)` | 附言 |
| `status` | `TINYINT` | 1 待处理、2 已同意、3 已拒绝、4 已失效 |
| `created_at` / `updated_at` / `handled_at` | `DATETIME(3)`（`handled_at` 可空） | |
| `pending_flag` | 生成列 `IF(status = 1, 1, NULL)` | 唯一索引 `uk_pending (from_user_id, to_user_id, pending_flag)` |

索引：`idx_to (to_user_id, status, id)` 查收到的待处理申请；`idx_from (from_user_id, id)` 查发出的申请。

**blocks**：`user_id`、`blocked_id`、`created_at`，主键 `(user_id, blocked_id)`。

**chat_groups**

| 列 | 类型 | 说明 |
|---|---|---|
| `id` | `BIGINT` PK | snowflake，与用户 ID 共用节点号空间，不会相同 |
| `name` | `VARCHAR(32)` | 接口限制 1～30 字符 |
| `avatar` | `VARCHAR(64)` | media 文件 ID |
| `notice` | `VARCHAR(500)` | 群公告，仅成员可见 |
| `owner_id` | `BIGINT` | 冗余，便于展示 |
| `join_mode` | `TINYINT` | 1 直接加入、2 需审核 |
| `mute_all` | `TINYINT` | 0 / 1 |
| `member_count` | `INT` | 在锁住群行的事务内维护 |
| `status` | `TINYINT` | 1 正常、2 已解散 |
| `version` | `INT` | 资料版本号，乐观锁 |
| `created_at` / `updated_at` / `dismissed_at` | `DATETIME(3)` | |

**group_members**

| 列 | 类型 | 说明 |
|---|---|---|
| `group_id` / `user_id` | `BIGINT` | 主键 `(group_id, user_id)`；索引 `idx_user (user_id, group_id)` |
| `role` | `TINYINT` | 1 成员、2 管理员、3 群主 |
| `muted_until` | `DATETIME(3) NULL` | |
| `inviter_id` | `BIGINT` | 邀请人，自己加入为 0 |
| `joined_at` | `DATETIME(3)` | |

**group_join_requests**：结构同 `friend_requests`。`group_id`、`user_id` 代替两个用户 ID，另有 `handled_by`（处理人）。唯一索引 `uk_pending (group_id, user_id, pending_flag)`，索引 `idx_group (group_id, status, id)`。

**outbox_events**：与 account 同构。

列表一律游标分页：

- 好友按 `friend_id`、成员按 `user_id`、我的群按 `group_id` 递增；
- 申请按 `id` 递减（snowflake 近似时间顺序）；
- 每页最多 100 条。

### 4.2 接口

对外接口（`social.v1.SocialService`，HTTP 与 gRPC，全部需要登录）：

| 方法 | HTTP | 说明 |
|---|---|---|
| `SendFriendRequest` | `POST /v1/friend-requests` | `user_id`、`message`；返回申请，或 `became_friends = true` |
| `ListReceivedFriendRequests` / `ListSentFriendRequests` | `GET /v1/friend-requests/received`、`/sent` | 游标分页，带对方资料 |
| `AcceptFriendRequest` / `RejectFriendRequest` | `POST /v1/friend-requests/{request_id}/accept`、`/reject` | 仅接收方 |
| `ListFriends` | `GET /v1/friends` | 游标分页，带资料 |
| `DeleteFriend` | `DELETE /v1/friends/{user_id}` | 幂等 |
| `BlockUser` / `UnblockUser` / `ListBlockedUsers` | `POST /v1/blocks`、`DELETE /v1/blocks/{user_id}`、`GET /v1/blocks` | |
| `CreateGroup` | `POST /v1/groups` | 名称、头像、公告、加群方式、初始成员 |
| `GetGroup` | `GET /v1/groups/{group_id}` | 非成员看不到公告；带本人角色 |
| `UpdateGroup` | `PATCH /v1/groups/{group_id}` | `version` 加 `optional` 字段（名称、头像、公告、加群方式、全员禁言） |
| `ListMyGroups` | `GET /v1/groups` | 游标分页，带本人角色 |
| `JoinGroup` | `POST /v1/groups/{group_id}/join` | 返回“已加入”或“待审核” |
| `ListGroupJoinRequests` | `GET /v1/groups/{group_id}/join-requests` | 群主、管理员 |
| `AcceptGroupJoinRequest` / `RejectGroupJoinRequest` | `POST /v1/group-join-requests/{request_id}/accept`、`/reject` | 群主、管理员 |
| `AddGroupMembers` | `POST /v1/groups/{group_id}/members` | 邀请好友，群主、管理员 |
| `ListGroupMembers` | `GET /v1/groups/{group_id}/members` | 仅成员，游标分页，带资料 |
| `RemoveGroupMember` | `DELETE /v1/groups/{group_id}/members/{user_id}` | |
| `SetGroupMemberRole` | `PUT /v1/groups/{group_id}/members/{user_id}/role` | 群主 |
| `MuteGroupMember` | `PUT /v1/groups/{group_id}/members/{user_id}/mute` | `duration_seconds`，0 为解除，最长 30 天 |
| `TransferGroupOwner` | `POST /v1/groups/{group_id}/transfer` | 群主，新群主必须是成员，原群主变为管理员 |
| `LeaveGroup` | `POST /v1/groups/{group_id}/leave` | 群主需先转让 |
| `DismissGroup` | `POST /v1/groups/{group_id}/dismiss` | 群主或系统管理员 |

内部接口见 3.5。

### 4.3 错误码

`social.v1.ErrorReason`，HTTP 状态码随错误类型确定：

| 原因 | HTTP |
|---|---|
| `CANNOT_TARGET_SELF` | 400 |
| `FRIEND_REQUEST_NOT_ALLOWED`、`NOT_FRIENDS`、`BLOCKED`、`GROUP_PERMISSION_DENIED`、`NOT_GROUP_MEMBER`、`MEMBER_MUTED`、`GROUP_MUTED` | 403 |
| `USER_NOT_FOUND`、`FRIEND_REQUEST_NOT_FOUND`、`GROUP_NOT_FOUND`、`GROUP_JOIN_REQUEST_NOT_FOUND`、`GROUP_MEMBER_NOT_FOUND` | 404 |
| `ALREADY_FRIENDS`、`REQUEST_ALREADY_HANDLED`、`ALREADY_GROUP_MEMBER`、`GROUP_FULL`、`GROUP_DISMISSED`、`GROUP_VERSION_CONFLICT`、`OWNER_CANNOT_LEAVE` | 409 |
| `RATE_LIMITED` | 429（带 `retry_after_seconds`） |
| `DEPENDENCY_UNAVAILABLE` | 503 |

## 5. 候选方案与权衡

| 问题 | 采用 | 未采用及原因 |
|---|---|---|
| 好友关系存储 | 一段关系两行 | 一行（较小 ID 在前）：查“我的好友”要 `OR` 两个方向或建两个索引，以后加备注没有位置 |
| 拉黑 | 独立表，不影响好友关系 | 旧基线把拉黑当作联系状态：拉黑与删除互相覆盖，状态机复杂（TD-19） |
| 群成员存储 | 成员表，`(group_id, user_id)` 主键 | 旧基线的 JSON 列：无法索引“我的群”，并发修改会丢更新 |
| 成员计数与满员检查 | 事务内锁群行后维护 | `UPDATE ... SET member_count = member_count + 1 WHERE member_count < 上限`：单人入群可以，批量邀请时要先知道其中多少人已是成员，仍需加锁 |
| 群资料并发修改 | 乐观锁（版本号） | 行锁：编辑跨越用户思考时间，不能持锁；最后写入者覆盖：静默丢失他人的修改 |
| 退群与移除 | 删除成员行，事件留痕 | 软删除：主键冲突，再次入群要“复活”旧行 |
| 成员列表缓存击穿 | singleflight + Redis 重建锁 | 只用 singleflight：多实例同时回源；逻辑过期：需要后台刷新任务，收益不大 |
| 列表补全资料 | 调用 account，失败时降级 | social 订阅 account 事件维护资料副本：多一份数据和同步逻辑，步骤 4 之前也没有发布器 |
| 互相申请 | 自动成为好友 | 留下两条待处理申请：用户需要再点一次同意，状态更复杂 |

## 6. 新增依赖与环境变化

- 不新增 Go 依赖：gRPC 客户端、服务发现都在 Kratos 和现有依赖中。
- compose 新增 `migrate-social` 任务，Makefile 的 `migrate-%` 已支持。
- `buf.yaml` 的枚举前缀例外加上 `api/social/v1/error_reason.proto`。
- e2e 需要同时启动 account 和 social。

## 7. 风险与验证

| 风险 | 应对 |
|---|---|
| 第一次服务间调用，Kratos 客户端可能有类似步骤 1 选择器那样的问题 | `pkg/balancer` 已修复已知问题；先写经服务发现调用 account 的集成测试，再写业务 |
| 抽取 `pkg/bloom` 改动 account | 键名与行为不变；account 的单元测试和 e2e 全部重跑 |
| 群行锁在大群高频进出时成为瓶颈 | 群规模中小；步骤 8 压测时观察锁等待 |
| 成员列表缓存删除失败，权限校验用到旧成员列表（被移除的人还能发言最多 30 分钟） | 删除失败记 WARN 并计入 TD；步骤 4 起经事件重试删除 |

验证方案：

- 单元测试：
  - biz 层用假 repo，覆盖申请规则表的每一行、互相申请、拉黑的效果、权限矩阵、满员、乐观锁冲突、转让与退群、解散、发言权限的各种拒绝原因。
  - `pkg/cache` 重建锁用 miniredis 测试抢锁、等待、超时回源。
- 集成测试（构建标签 `e2e`）：
  - `pkg/bloom` 的失败分支，补 TD-27；
  - social 经 Consul 调用 account。
- e2e：两个服务一起运行，覆盖好友完整流程、拉黑、建群与邀请、审核入群、角色与禁言、内部发言权限、资料乐观锁冲突、满员、解散。
- `EXPLAIN` 验证列表与权限查询走索引，结果记录在 `data-model.md`。

## 8. 待开发者确认

以下几点超出旧基线或有多种合理做法，按推荐方案写入了本文：

1. 单聊要求双方是好友（旧基线只检查拉黑和禁用）。
2. 拉黑不删除好友关系，任何一方拉黑后双方都不能单聊；发消息时返回 `BLOCKED`，发送方能知道自己被拉黑或拉黑了对方。
3. 双方互相申请时自动成为好友。
4. 新增邀请好友入群、建群时带初始成员、转让群主（旧基线群主不能转让，建群时只有群主一人）。
5. 系统管理员可以解散任意群；禁用群的功能不做。
6. 抽取 `pkg/bloom`（改动 account 的 data 层），`pkg/cache` 增加重建锁。
7. account 不可用时，列表接口降级返回不带资料的数据；申请好友时返回 503。

## 9. 实施说明

2026-11-19 实施完成，与上文的差异和实施中的决定：

| 项 | 实施 | 原因 |
|---|---|---|
| 错误原因 `CANNOT_ADD_SELF` | 改名为 `CANNOT_TARGET_SELF` | 拉黑、删除、移除、禁言、设置角色、转让给自己都用它，“ADD”不准确 |
| 新增 `GROUP_MEMBER_NOT_FOUND`（404） | 操作对象不是群成员 | 与“操作者不是成员”（`NOT_GROUP_MEMBER`，403）区分 |
| 内部方法 `ListGroupMembers` | 改名为 `ListAllGroupMembers` | 与对外的分页接口 `ListGroupMembers` 同属 `social.v1` 包，请求消息会重名 |
| 新增事件 `GroupJoinRequestCreated` | 需审核的群收到入群申请时写入 | 群主、管理员需要被通知，与 `FriendRequestCreated` 对称 |
| 公共中间件链 | 放在 `pkg/bootstrap/middleware.go` | `pkg/bootstrap` 本来就是各服务的启动装配包；放进 `pkg/middleware` 会与 Kratos 的 `middleware` 包重名 |
| 单聊发言权限 | 2 次主键查询（拉黑用行构造器 `IN` 一次查两个方向） | `EXPLAIN` 确认为主键上的两个点范围，见 data-model.md |
| 并发申请 | 同一对用户并发申请撞上 `uk_pending` 时整个事务重试一次；互相申请同时提交留下两条申请时，同意任何一条都会把另一条一并标记为已同意 | 不加锁也不留下多余的待处理申请 |
| 锁群行后的读取 | 事务里先 `SELECT ... FOR UPDATE` 锁群行，之后的普通读取才建立快照 | 能读到之前持锁事务提交的修改；条件更新（`WHERE status = 1`）再兜底一次 |
| 群资料版本号 | 转让群主、解散也加一 | 都会改变 `chat_groups` 中客户端可见的字段 |
| 建群时初始成员含创建者本人 | 忽略，不报错 | 客户端多传一个 ID 不值得失败 |
| 邀请时已是成员的人 | 先排除，再要求其余的人都是操作者的好友 | 已在群里的人不是好友也不影响 |
| 限流 | Redis 故障时放行（与 account 的登录限流一致） | 用于防骚扰，不值得为它让功能不可用 |
| 就绪探针 | 不检查 account | account 故障时 social 降级运行，不应被摘掉流量 |

实施中发现的问题：

- TD-27 的集成测试发现 `BF.EXISTS` 在 RESP3 下返回布尔值，原 `user_bloom.go` 按整数读取，过滤器从步骤 2 起没有拦截过任何 ID。抽取到 `pkg/bloom` 时一并修复，见 [lessons/redis-resp3-reply-types.md](../lessons/redis-resp3-reply-types.md)。
- 新增 TD-28（布隆过滤器加入重试期间的假阴性）、TD-29（拉黑与好友申请并发）；TD-25 扩大到群成员列表缓存，优先级调为中；TD-26 扩大到 `SocialInternalService`。

验证结果：

- 单元测试：`app/social/internal/biz`（申请规则表每一行、互相申请、并发重试、拉黑效果、权限矩阵 36 种组合、满员、乐观锁、转让与退群、解散、发言权限的各种拒绝原因），`pkg/cache` 重建锁（抢锁、等待别的实例回填、等到空值标记、超时回源、释放时校验持有者）。把权限判断故意改坏后测试失败，确认测试有效。
- 集成测试与 e2e（`make e2e`，account 与 social 同时运行）：`bloom_test.go` 7 项、`social_test.go` 4 项（好友完整流程、拉黑、建群（带初始成员）、审核入群、角色与禁言、内部发言权限、乐观锁冲突、满员、转让、解散），连同 account 与服务发现共 17 项通过，连续运行两次结果一致。
- `EXPLAIN`：在临时库中造数据后核对，结果记录在 [data-model.md](../architecture/data-model.md#22-socialmarschat_social迁移-000001_init)。
