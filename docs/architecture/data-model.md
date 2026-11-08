# 数据模型

> 权威来源：`internal/model/*.go`（表结构）、`pkg/enum/**`（状态值）。表由 `internal/dao/gorm.go` 在启动时 `AutoMigrate` 自动创建，仓库中没有 SQL 迁移脚本。

## 1. 表关系

```
user_info (U...)  ──1:N──  user_contact  ──N:1──  user_info / group_info
     │                    （每条记录 = 某用户视角下的一个联系人或群）
     ├──1:N──  contact_apply  ──N:1──  user_info / group_info
     ├──1:N──  session        ──N:1──  user_info / group_info（按 send_id 归属）
     └──1:N──  message        ──N:1──  user_info / group_info（receive_id）

group_info (G...)  members 列以 JSON 数组保存成员 uuid，member_cnt 冗余计数
```

表之间没有数据库外键，关联完全靠 uuid 字符串和首字母前缀（见 [system.md](system.md#52-id-规则)）。

## 2. 表结构要点

| 表 | 关键字段 | 说明 |
|---|---|---|
| `user_info` | `uuid`、`telephone`(char 11)、`password`(char 18，明文)、`nickname`、`avatar`、`gender`、`birthday`、`signature`、`is_admin`、`status`、`deleted_at` | 手机号是登录名；`last_online_at`、`last_offline_at` 未被写入 |
| `group_info` | `uuid`、`name`、`notice`、`members`(JSON)、`member_cnt`、`owner_id`、`add_mode`、`avatar`、`status`、`deleted_at` | 解散群聊只写 `deleted_at`，不会把 `status` 置为 2 |
| `user_contact` | `user_id`、`contact_id`、`contact_type`、`status`、`deleted_at` | 好友关系双向各一条；群关系只有“用户→群”一条 |
| `contact_apply` | `uuid`、`user_id`(申请人)、`contact_id`(被申请的用户或群)、`contact_type`、`status`、`message`、`last_apply_at` | 同一对 (申请人, 目标) 复用一条记录 |
| `session` | `uuid`、`send_id`(会话所有者)、`receive_id`、`receive_name`、`avatar`、`deleted_at` | 会话按所有者单向存储：A 打开与 B 的会话只创建 A 的记录；`last_message`、`last_message_at` 未被写入 |
| `message` | `uuid`、`session_id`、`type`、`content`、`url`、`send_id`、`send_name`、`send_avatar`、`receive_id`、`file_*`、`status`、`av_data`、`created_at` | 单聊按 (send_id, receive_id) 双向查询，群聊按 receive_id 查询；`send_at` 未被写入 |

## 3. 枚举

| 枚举 | 取值 | 代码位置 |
|---|---|---|
| 用户状态 `user_info.status` | 0 正常、1 禁用 | `user_status_enum` |
| 群状态 `group_info.status` | 0 正常、1 禁用、2 解散（未使用） | `group_status_enum` |
| 加群方式 `group_info.add_mode` | 0 直接加入、1 需审核 | `add_mode_enum` |
| 联系类型 `contact_type` | 0 用户、1 群聊 | `contact_type_enum` |
| 联系状态 `user_contact.status` | 0 正常、1 被拉黑、2 拉黑、3 被删除、4 删除、5 被禁言（未使用）、6 退出群聊、7 被踢出群聊（未使用） | `contact_status_enum` |
| 申请状态 `contact_apply.status` | 0 申请中、1 通过、2 拒绝、3 拉黑 | `contact_apply_status_enum` |
| 消息类型 `message.type` | 0 文本、1 语音（未实现）、2 文件、3 音视频通话信令 | `message_type_enum` |
| 消息状态 `message.status` | 0 未发送、1 已发送 | `message_status_enum` |

> 注意：`internal/model/user_contact.go` 字段注释中 1～4 的含义与枚举顺序相反。实际写入逻辑与枚举一致（例如拉黑时操作者记 2、对方记 1），**以枚举为准**（TD-19）。

状态流转见 [contact.md](../product/modules/contact.md)。

## 4. Redis 键

所有缓存键的 TTL 为 `constants.REDIS_TIMEOUT` 分钟（当前为 1）。值均为响应结构体数组的 JSON 字符串。

| 键 | 内容 | 写入方 | 失效方 |
|---|---|---|---|
| `auth_code_<手机号>` | 6 位短信验证码，TTL 1 分钟 | `sms.VerificationCode` | 登录或注册校验通过后删除 |
| `contact_user_list_<uuid>` | 好友列表 | `GetUserList` | 删除好友、通过好友申请（只清操作者一方） |
| `my_joined_group_list_<uuid>` | 我加入的群 | `LoadMyJoinedGroup` | 解散群、移除成员（按前缀全清）；退群、直接进群、通过加群申请的清理键名有误，实际无效 |
| `contact_mygroup_list_<uuid>` | 我创建的群 | `LoadMyGroup` | 建群、解散（只清群主）、删除群聊（按前缀全清） |
| `session_list_<uuid>` | 单聊会话列表 | `GetUserSessionList` | 创建会话、删除会话 |
| `group_session_list_<uuid>` | 群聊会话列表 | `GetGroupSessionList` | 创建/删除会话、退群、解散（只清群主）、删除群聊与移除成员（按前缀全清） |
| `message_list_<发送方>_<接收方>`、`group_messagelist_<群>` | 消息列表 | **无**（写入代码已注释） | — |
| `group_info_<群>`、`group_memberlist_<群>`、`user_info_<uuid>`、`session_<a>_<b>*` | 详情缓存 | **无**（只读不写） | — |

后两行的键当前不会出现，相关读取分支总是回源 MySQL，转发服务里的“追加消息到缓存”分支也不会触发（TD-09）。

服务正常退出时会执行 `DeleteAllRedisKeys`，清空当前 Redis 库的**所有**键（TD-06）。
