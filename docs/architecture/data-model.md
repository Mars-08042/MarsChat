# 数据模型

> 本文记录**新后端**的库、表、索引和 Redis 键，随重构步骤更新，最近一次更新于步骤 1（2026-11-18）。设计原则见 [后端重构计划](../plans/active/backend-refactor.md#数据库与缓存设计原则)。旧基线的表结构放在文末附录，只作功能参考。

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

暂无。表结构从步骤 2 起用版本化 SQL 迁移创建，每张表在这里记录字段、索引及对应的查询。

## 3. Redis 键

暂无。键名、TTL 和失效方式从步骤 2 起在这里记录。

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
