# 设计：核心消息链路（重构步骤 4）

- 状态：已采纳（2026-11-19 开发者确认；实施差异见第 9 节） | 日期：2026-11-19 | 关联：[重构计划步骤 4](../plans/completed/backend-refactor.md#4-核心消息链路各服务单实例)、[ADR-0005](../architecture/adr/0005-distributed-architecture.md)、[ADR-0006](../architecture/adr/0006-message-sync-model.md)、[social 设计](social-service.md)、旧基线 [chat-message.md](../product/modules/chat-message.md)

## 1. 背景与目标

旧基线的消息链路有这些问题：

- 在线连接表在进程内存里，只能单实例运行；
- kafka 模式在多实例下会丢投；
- 消息没有序号、确认和补拉，离线消息只能在打开会话时全量读取；
- 会话没有“最后一条消息”和未读数。

步骤 4 按 ADR-0006（读扩散、会话内 seq、游标补拉）实现消息主链路。每个服务先只跑一个实例，但设计上不能妨碍步骤 5 的多实例。

目标：

- **access**：
  - WebSocket 接入、鉴权、心跳；
  - 每个连接一个有界发送队列，满了就断开慢连接；
  - 帧协议用 proto 定义；
  - Redis 在线路由；
  - 账号被封禁或会话被吊销时断开连接。
- **message**：
  - 会话模型：单聊、群聊；
  - 按 `client_msg_id` 幂等；
  - 事务内用行锁分配 seq，消息与 Outbox 在同一事务；
  - 历史分页、会话列表（最后一条消息、未读数）、已读位置；
  - 按 seq 补拉，三种结果。
- **Outbox 发布器与 Kafka**：
  - 实现 `pkg/outbox` 的发布端。account、social、message 都启用，步骤 2、3 积压的事件从此真正发出；
  - topic 由初始化任务显式创建。
- **delivery 最小实现**：
  - 消费消息事件，消费幂等；
  - 查在线路由，推送给在线连接，排除发起消息的那个连接；
  - 消费 account 事件，通知 access 断开连接。
- **e2e**：用 Go WebSocket 客户端覆盖发送确认、重复发送只存一条、断线补齐、多端同步、已读与未读。

不在本步骤：

- 多实例、APISIX、重试队列与死信队列、熔断（步骤 5）；
- 文件与图片消息（步骤 6）、通话信令（步骤 7）；
- 撤回、编辑：ADR-0006 已定为进入同一 seq 序列的会话事件，主线之后再排；
- 群系统消息（“某某加入了群聊”）、@ 提醒、正在输入、消息搜索、删除会话与清空历史；
- 好友申请、入群申请等 social 事件的实时通知（步骤 5 delivery 完整实现时做）。

## 2. 需求与约束

- 消息按会话只存一份；会话内 seq 从 1 开始连续递增，没有空洞（ADR-0006）。
- 写库成功才确认；实时推送尽力而为，可靠性靠按 seq 补拉。
- 群不超过 500 人（social 的 `policy`），不支持超大群。
- 发言权限以 social 为准（`CheckSendPermission`），message 不复制好友和拉黑关系。
- 客户端以浏览器为主（前端之后用 React 重写），所以要考虑浏览器 WebSocket API 的两个限制：
  - 握手时**不能设置自定义请求头**；
  - **不能主动发送** WebSocket 的 ping 控制帧。
- 浏览器的多个标签页共用一个令牌，也就是同一个登录会话 ID（令牌里的 `sid`，下文称“登录会话”，与聊天的“会话”区分），会建立多条连接。在线路由和“排除发起端”因此按**连接**区分，不按登录会话区分。

## 3. 方案

### 3.1 模块划分与端口

| 位置 | 内容 | 端口 | 依赖组件 |
|---|---|---|---|
| `app/access` | WebSocket 接入、连接管理、在线路由 | HTTP / WebSocket 18000；gRPC **19100**（见下文） | Redis |
| `app/message` | 会话、消息、已读、补拉；消费 social 事件维护群成员 | 18030 / 19030 | MySQL `marschat_message`、Redis、Kafka |
| `app/delivery` | 消费事件、查路由、推送、踢连接 | gRPC 19040（只用于健康检查和注册） | Redis、Kafka |
| `api/access/v1` | `frame.proto`（WebSocket 帧）、`internal.proto`（`Push`、`Kick`） | | |
| `api/message/v1` | `message.proto`（对外）、`internal.proto`（`SendMessage`）、`error_reason.proto`、`events.proto` | | |
| `pkg/outbox`（扩展） | 发布器 `Publisher` | | |
| `pkg/kafka`（新增） | 生产者与消费者的公共封装、记录头约定 | | |
| `pkg/auth`（扩展） | 校验原始令牌字符串的 `Verifier`，与中间件共用解析选项 | | |

**access 的 gRPC 端口**：delivery 要直连 access 实例推送，所以 access 需要一个 gRPC 端口。按 `19xx0` 规则它应该是 19000，但 19000 已经分给步骤 6 的 MinIO（9000 + 10000，容器端口规则不能改），因此改用 19100（待确认，见第 8 节）。已确认本机当前没有程序占用。

### 3.2 整体链路

```
发送：
客户端 ─WS 帧 SendMessage─→ access ─gRPC SendMessage─→ message
                                                       ├─ gRPC CheckSendPermission → social
                                                       └─ 事务：锁会话行分配 seq、写消息、更新发送者已读、写 Outbox
客户端 ←─WS 帧 SendAck─── access ←── 确认（会话 ID、seq）─┘

投递：
message 发布器 ─→ Kafka marschat.message.events（键 = 会话 ID）
delivery 消费 ─→ 确定接收者（单聊两人；群聊经 social ListAllGroupMembers，走缓存）
              ─→ Redis 在线路由：用户 → 连接 → access 实例地址
              ─→ 按实例分组，gRPC Push 到对应 access（排除发起连接）─→ WS 帧 MessagePush

补拉：
客户端重连 ─HTTP→ message ListConversations（每个会话的 max_seq 与本人已读位置）
           ─HTTP→ 对本地有缺口的会话调用 GetDifference
```

消息、会话列表、历史、补拉、已读都由 message 提供。按 ADR-0005，access 只承载 WebSocket，不聚合 HTTP。

### 3.3 access

**连接与鉴权**：

- 客户端连接 `GET /ws`，用子协议选择帧编码：
  - `marschat.v1.proto`（默认）：二进制 protobuf；
  - `marschat.v1.json`：protojson，便于用命令行工具手工调试。
- 浏览器握手时不能带 `Authorization` 头，所以**第一帧必须是 `Auth{access_token}`**，限 10 秒内发送。令牌不放在 URL 里，因为 URL 会进入网关和代理的访问日志。
- 校验与 HTTP 接口相同：验签、声明、吊销名单，见 `pkg/auth` 的 `Verifier`。
  - 失败：返回 `Error` 帧，再以关闭码 4001 关闭；
  - 成功：生成连接 ID（63 位随机数），登记在线路由，返回 `AuthOK{user_id, session_id, connection_id, heartbeat_interval}`。
- 连接**不随访问令牌过期而断开**（否则客户端每 15 分钟要重连一次）。失效由两条路径保证：
  - **立即**：delivery 收到 `SessionRevoked`、`UserBanned` 事件后调用 `Kick`，access 发送 `Kicked` 帧并以 4002 关闭；
  - **兜底**：每次心跳都查一次吊销名单。事件链路故障时，最迟一个心跳周期（30 秒）后断开。

**心跳**：

- 客户端每 30 秒发一次 `Ping`，服务端回 `Pong`，同时续期路由 TTL 并检查吊销。
- 90 秒内没有收到任何帧就关闭连接。
- 用应用层帧而不用 WebSocket 控制帧：浏览器不能主动发 ping，客户端也需要靠 `Pong` 发现服务端失联。

**收发与保护**：

| 项 | 做法 |
|---|---|
| 发送队列 | 每个连接一个容量 256 的有界队列，由单独的写协程发送，单帧写超时 10 秒；队列满时以 4003 断开慢连接，客户端重连后补拉 |
| 帧大小 | 读取上限 64 KiB |
| 上行帧频率 | 每个连接每秒最多 30 帧（`policy`，热更新），超出以 4008 关闭。只用进程内计数器，作用是保护 access 自身；按用户的发消息频率由 message 用 Redis 统一限制（见 3.4） |
| 发消息 | 读协程按顺序处理 `SendMessage`，调用 message（超时 3 秒），返回 `SendAck` 或带同一 `request_id` 的 `Error`。同一连接内按发送顺序确认 |
| 推送 | `Push(connection_ids, frame)` 把帧放进对应连接的队列，返回实际投递的连接数；不在本实例的连接直接忽略 |
| 停机 | 以 1001 关闭全部连接并删除它们的路由。先从 Consul 摘除、通知客户端重连属于步骤 5 |

关闭码在 `frame.proto` 的注释和 `data-flow.md` 中列出：

- 4001 未认证；
- 4002 被踢；
- 4003 慢连接；
- 4008 帧频率超限；
- 1001 服务停机。

**Kratos 的注意点**：Kratos 的 HTTP 服务会给每个请求的 context 加上服务端超时（默认 1 秒）。WebSocket 处理函数注册在 Kratos HTTP 服务上，但连接的生命周期不能用请求 context，要改用 access 自己的上下文。实施时先写测试确认这一点。

### 3.4 message

**会话**：

| 类型 | 会话 ID | 创建时机 |
|---|---|---|
| 单聊 | 新的 snowflake；唯一索引 `uk_pair (user_low, user_high)` | 第一条消息发送时，在同一事务中创建会话和两人的成员行 |
| 群聊 | **直接用群 ID**：节点号由所有服务共享，各服务生成的 ID 不会重复 | 消费 `GroupCreated` 时创建；发消息时如果事件还没到，就先建会话行 |

- 单聊成员固定是两个人，所以在发送时按需创建，不需要订阅 `FriendAdded`。social 设计里写的“message 消费 `FriendAdded` 准备单聊会话”因此不再需要，实施说明中记录这个差异。
- 删除好友后会话保留：历史仍可查看，但不能再发消息（social 权限校验拒绝）。

**群成员**：message 维护自己的一份成员表，用来做读接口的权限判断和会话列表，数据来自 social 事件：

| 事件 | 处理 |
|---|---|
| `GroupCreated` | 建会话行，插入全部初始成员，已读位置 0 |
| `GroupMembersJoined` | 插入成员行，已读位置取当时会话的 `max_seq`：能看到历史，但历史不算未读 |
| `GroupMemberLeft` | 删除成员行：会话从该用户的列表中消失，不能再读历史和补拉 |
| `GroupDismissed` | 不改成员：会话保留在列表里，客户端从群资料看到“已解散”，发消息被 social 拒绝 |

- 同一个群的事件在同一分区（键为群 ID），按顺序处理。SQL 本身是幂等的：
  - 插入用 `INSERT IGNORE`；
  - 删除对不存在的行无影响；
  - 重复投递表现为“一段前缀重放”，结果仍然正确。
- **发消息时不写成员行**，成员只随事件变化。原因：如果发送时“补建”成员行，就可能和并发的 `GroupMemberLeft` 竞争，把刚被移除的人重新加回来。
- 代价是事件延迟（通常在 1 秒以内）带来的短暂不一致：
  - 刚入群的人，要等事件处理完才能读历史；
  - 刚被移除的人，在这段时间内还能读到新消息。
  - 发消息始终以 social 的实时校验为准，不受影响。

**发送**（内部接口 `SendMessage`，由 access 调用）：

```
1. protovalidate 校验；按用户限流（Redis 滑动窗口，policy 可热更新；Redis 故障时放行并记 WARN）
2. 解析会话：群 → 会话 ID = 群 ID；单聊 → 按 (较小 ID, 较大 ID) 查会话 ID，可能还不存在
3. 幂等快速路径：会话已存在时，按 (会话 ID, 发送者, client_msg_id) 查已有消息，命中就返回首次的确认
4. 权限：调用 social CheckSendPermission（事务外）。拒绝时原样返回 social 的错误
   （NOT_FRIENDS、BLOCKED、MEMBER_MUTED 等）；social 不可用时返回 503，不在无法确认时放行
5. 事务：
   a. 会话不存在时创建：INSERT ... ON DUPLICATE KEY UPDATE（单聊同时插入两人的成员行）
   b. SELECT max_seq FROM conversations WHERE id = ? FOR UPDATE，锁住会话行
   c. INSERT 消息，seq = max_seq + 1；撞上唯一键 uk_client（并发重发）时回滚，读出已有消息返回
   d. UPDATE conversations SET max_seq = seq
   e. 发送者的成员行 read_seq = seq（行存在时）。回复视为已读到这里
   f. Outbox：MessageCreated，键为会话 ID
6. 提交后唤醒发布器，返回确认（会话 ID、seq、时间）
```

- **幂等唯一键是 `(conversation_id, sender_id, client_msg_id)`**，比 ADR-0006 写的（发送者，`client_msg_id`）多了会话 ID：按数据库原则，索引要以分片键开头，将来按会话分片时唯一约束仍然有效。客户端用 UUID 生成 `client_msg_id`，跨会话不会重复。
- 重发时内容不同：返回首次的确认，忽略新内容。
- 创建会话用 `ON DUPLICATE KEY UPDATE` 而不用 `INSERT IGNORE`。`INSERT IGNORE` 遇到重复时加的是共享锁，两个事务随后都要在同一行上升级为排他锁，会互相死锁。
- 第 c 步撞唯一键时整个事务回滚，`max_seq` 不变，seq 不会出现空洞。
- **不另设消息 ID**：`(会话 ID, seq)` 已经唯一，撤回、引用等以后都用它定位。

**读接口**（对外，HTTP 与 gRPC，都要登录；不是会话成员时一律返回 `404 CONVERSATION_NOT_FOUND`，防止枚举会话 ID）：

| 方法 | 行为 |
|---|---|
| `ListConversations` | 按会话 ID 游标分页，每页最多 100 条。每项包含：类型、对方资料或群资料、`max_seq`、本人 `read_seq`、未读数 `max(0, max_seq − read_seq)`、单聊对方的已读位置（已读回执）、最后一条消息（按 `(会话 ID, max_seq)` 主键批量取，不冗余存储）。对方资料调 account `BatchGetUsers`、群资料调 social `BatchGetGroups`，失败时降级为空，与 social 列表的做法一致。按最后消息时间排序由客户端完成，理由见第 5 节 |
| `GetConversation` | 单个会话，字段同上。客户端收到未知会话的推送时调用 |
| `ListMessages` | 历史：`before_seq`（0 表示从最新开始）向前翻页，每页最多 100 条，按 seq 降序 |
| `GetDifference` | 补拉：返回 `from_seq` 之后的消息，按 seq 升序，每次最多 `limit` 条（默认 100，最多 200）。三种结果见下 |
| `MarkRead` | `read_seq` 只增不减，且不超过 `max_seq`；位置有变化时在同一事务中写 `ReadPositionUpdated` 事件 |

**补拉的三种结果**（参考 teamgram 的 getDifference）：

| 结果 | 条件 | 客户端处理 |
|---|---|---|
| `COMPLETE` | 已经返回到 `max_seq`，或本来就没有缺口 | 补齐完成 |
| `SLICE` | 缺口在阈值内，但超过本次 `limit` | 用返回的 `next_from_seq` 继续拉 |
| `TOO_LONG` | `max_seq − from_seq` 超过阈值（默认 1000，`policy` 热更新） | 丢弃这个会话的本地缓存，用 `ListMessages` 重新加载最新一页 |

**未读与已读**：

- 未读数 = `max_seq − read_seq`。自己发的消息会把自己的已读位置推进到这条消息。
- 本人在一台设备上读过之后，其他设备经 `ReadPositionUpdated` 推送同步未读数；单聊同时推给对方，作为已读回执。
- 群聊不做逐人已读（ADR-0006）。
- 撤回、编辑以后进入 seq 序列时，未读数的算法要重新考虑（只计普通消息）。

### 3.5 delivery

消费组 `marschat-delivery`，消费 `marschat.message.events` 和 `marschat.account.events`：

| 事件 | 接收者 | 动作 |
|---|---|---|
| `MessageCreated` | 单聊：两人（事件中带上）；群聊：social `ListAllGroupMembers`（走成员列表缓存） | 推送 `MessagePush`，排除事件中的发起连接。发送者的其他设备、其他标签页都能收到 |
| `ReadPositionUpdated` | 本人；单聊加上对方 | 推送 `ReadPush`，不排除发起端（已读位置只增不减，客户端收到旧值时忽略） |
| `SessionRevoked` | 该登录会话的所有连接 | `Kick` |
| `UserBanned` | 该用户的所有连接 | `Kick` |

- 查路由：对每个接收者 `HGETALL` 一次，用 pipeline 合并成一次往返。按 access 实例地址分组后，每个实例调用一次 `Push`。
- 按路由里的地址**直连** access 实例，不经 Consul 负载均衡（ADR-0005）。客户端连接按地址缓存；连续失败摘除、监听注册中心变化留到步骤 5。
- 推送失败只记日志，不重试（ADR-0006）。
- 群成员扩散在计划里原属步骤 5。它只是“取成员列表 + 查路由”，没有它群消息就做不到实时，所以提前到本步骤（待确认）。

### 3.6 在线路由

| 键 | 类型 | 内容 |
|---|---|---|
| `access:route:{user_id}` | Hash | 字段为连接 ID，值为 `{session_id}\|{access gRPC 地址}`；**字段级 TTL** 90 秒 |

- **登记**：`HSETEX key PX 90000 FIELDS 1 {conn_id} {value}`。每次心跳也重新执行一次 `HSETEX`，路由丢失（例如 Redis 重启）后能自动恢复。
- **注销**：连接关闭时 `HDEL`。连接 ID 是每条连接独有的，不会误删别的连接的路由，不需要“比较后删除”的脚本。
- **用字段级 TTL 而不是整键 TTL**：teamgram 给整个 Hash 设 TTL，一台设备崩溃后，只要该用户还有别的设备在续期，崩溃设备的字段就一直留着。Redis 7.4 起支持 Hash 字段过期，本项目用 Redis 8.8.4，go-redis v9.23 已支持 `HSETEX` 和 `HPEXPIRE`。
- **踢登录会话**：读出该用户的全部字段，筛出登录会话 ID 匹配的连接，再按实例分组调用 `Kick`。
- 推送时的连接选择以路由为准。access 进程崩溃后，路由最多残留 90 秒，期间推送失败只记日志。

### 3.7 Outbox 发布器

`pkg/outbox.Publisher` 实现为 Kratos 的 `transport.Server`，随服务启停。account、social、message 都启用。

- **每个服务同时只有一个实例在发布**：
  - 用 Redis 租约 `outbox:lease:{服务名}` 控制：`SET NX PX 10000`，每 3 秒续期，持有者为实例 ID；
  - 没拿到租约的实例定期重试抢占。
  - 这样做的原因：要保证同一个键的事件按写入顺序发布，同时不在数据库事务里调用 Kafka。
- **发布循环**：
  - 唤醒条件：被事务提交唤醒，或轮询间隔（500 毫秒）到期；
  - `SELECT ... WHERE published_at IS NULL ORDER BY id LIMIT 200`；
  - `ProduceSync`（`acks=all`、幂等生产者）；
  - `UPDATE ... SET published_at = ? WHERE id IN (...)`；
  - 取满一批就继续取。
- **低延迟**：数据层的事务提交成功后，用非阻塞的 channel 发送唤醒发布器，平时不靠高频轮询。
- **故障**：
  - Kafka 不可用时退避重试（1 秒起，最长 30 秒），事件留在表里，只会延迟、不会丢；
  - 发布后、标记前崩溃会重复发布，消费方按幂等处理。
- **清理**：每小时删除一批发布超过 7 天的记录（每批 1000 行），用已有的 `idx_unpublished (published_at, id)` 索引。
- **与计划的差异**：计划在步骤 5 用 `FOR UPDATE SKIP LOCKED` 支持多实例发布，这有两个问题：
  - 多个实例并发发布不同的行，同一个键的事件可能乱序；
  - 要在持有行锁的事务里调用 Kafka。

  租约方案在多实例下同样成立，建议替代（待确认）。单个发布者的吞吐（每批 200 条同步发送）对本项目足够；以后不够时，可以按键的哈希把租约拆成 N 份。

记录格式：键为 `event_key`，值为 `payload`，记录头包括：

- `event_type`：proto 全名；
- `event_id`：`{服务名}:{outbox id}`，用于消费幂等；
- `source`：服务名。

### 3.8 Kafka topic 与消费

| topic | 分区 | 生产方 | 消费方（消费组） |
|---|---|---|---|
| `marschat.account.events` | 3 | account | delivery（`marschat-delivery`）：踢连接 |
| `marschat.social.events` | 6 | social | message（`marschat-message`）：群成员；social（`marschat-social-cache`）：删缓存，见 3.9 |
| `marschat.message.events` | 12 | message | delivery（`marschat-delivery`）：推送 |

- 副本数为 1（本地单 broker），保留 7 天。
- compose 新增一次性任务 `kafka-init`：用镜像自带的 `kafka-topics.sh --create --if-not-exists` 创建 topic，随 `make up` 执行后退出。broker 已关闭自动建 topic。
- **消费者 `pkg/kafka.Consumer`**：
  - 也是 `transport.Server`；
  - 关闭自动提交，配合 `BlockRebalanceOnPoll`；
  - 每次 poll 后按顺序处理记录，然后 `CommitUncommittedOffsets`，再 `AllowRebalance`。这是 franz-go 文档推荐的组合。
- **处理器**：按 `event_type` 注册。
  - 未注册的类型：跳过，便于以后新增事件；
  - 解码失败：记 ERROR 并跳过；
  - 处理器返回错误：原地退避重试（100 毫秒起，最长 5 秒），直到成功或服务停止。群成员这类投影必须按顺序生效，不能跳过。
  - 重试队列与死信队列在步骤 5 实现。在那之前，持续失败的事件会阻塞所在分区，代价记入风险。
  - delivery 的处理器不返回错误：查路由、推送失败都只记日志（推送尽力而为），不阻塞后面的消息。原地重试只用于 message 的成员投影和 social 的缓存删除。
- **消费幂等**：
  - message 的成员投影：靠 SQL 本身幂等，同分区有序（见 3.4）；
  - delivery：推送前执行 `SET NX delivery:done:{event_id} EX 3600`，已存在就跳过，避免重复推送。先占位后推送，崩溃时宁可漏推（靠补拉兜底），也不重复；
  - 踢连接：本身幂等。

### 3.9 social 经事件重试删除缓存（TD-25 的 social 部分）

发布器和消费者就绪后，social 用消费组 `marschat-social-cache` 消费自己的 topic，收到群事件时再删一次 `social:group:{id}` 和 `social:group:members:{id}`。

- Redis 故障时，消费者原地重试，直到删除成功。
- 事务后的同步删除保留，作为快速路径；事件这一路负责兜底，覆盖“删除失败就要等 30 分钟 TTL”的问题。成员列表用于发言权限校验，所以这一项值得在本步骤处理（待确认）。
- account 的资料缓存没有对应的变更事件，TD-25 的 account 部分保持不变。

### 3.10 配置与热更新

各服务沿用 account 的方式：`policy` 段热更新，其余修改后重启生效。共用的配置消息加在 `pkg/conf`：

- `Kafka`：broker 列表，默认 `${KAFKA_BROKERS:127.0.0.1:19092}`；
- `Outbox`：轮询间隔、批量、保留天数。

| 服务 | 配置段 |
|---|---|
| access | `auth`、`message`（地址、超时 3 秒）、`websocket`（鉴权超时 10 秒、心跳间隔 30 秒、心跳超时 90 秒、发送队列 256、写超时 10 秒、最大帧 64 KiB）、`policy`（每连接每秒 30 帧） |
| message | `auth`、`kafka`、`outbox`、`social` 与 `account`（地址、超时）、`policy`（每人每 10 秒最多发 30 条消息、补拉 `limit` 上限 200、`TOO_LONG` 阈值 1000） |
| delivery | `kafka`、`social`（地址、超时）、`push`（调用 access 的超时 2 秒） |
| account、social | 新增 `kafka`、`outbox` |

## 4. 数据结构与接口

### 4.1 表（库 `marschat_message`，迁移 `000001_init`）

时间列 `DATETIME(3)`，按 UTC 读写。

**conversations**

| 列 | 类型 | 说明 |
|---|---|---|
| `id` | `BIGINT` PK | 群聊为群 ID，单聊为 snowflake |
| `type` | `TINYINT` | 1 单聊、2 群聊 |
| `user_low` / `user_high` | `BIGINT NULL` | 仅单聊；唯一索引 `uk_pair (user_low, user_high)`，群聊两列为 NULL，不占唯一约束 |
| `max_seq` | `BIGINT` | 在锁住本行的事务内递增 |
| `created_at` / `updated_at` | `DATETIME(3)` | |

**conversation_members**

| 列 | 类型 | 说明 |
|---|---|---|
| `conversation_id` / `user_id` | `BIGINT` | 主键 `(conversation_id, user_id)`；索引 `idx_user (user_id, conversation_id)` 用于会话列表 |
| `read_seq` | `BIGINT` | 已读位置，只增不减 |
| `joined_at` / `updated_at` | `DATETIME(3)` | |

**messages**

| 列 | 类型 | 说明 |
|---|---|---|
| `conversation_id` / `seq` | `BIGINT` | 主键 `(conversation_id, seq)`，以分片键开头；历史和补拉都是主键范围扫描 |
| `sender_id` | `BIGINT` | |
| `client_msg_id` | `VARCHAR(64)`，字符集 `ascii` | 唯一索引 `uk_client (conversation_id, sender_id, client_msg_id)` |
| `content` | `BLOB` | `message.v1.MessageContent` 的 protobuf 编码；文本最多 4000 字符，utf8mb4 下不超过 16 KB |
| `created_at` | `DATETIME(3)` | |

**outbox_events**：与 account、social 同构。

### 4.2 WebSocket 帧（`api/access/v1/frame.proto`）

```proto
message ClientFrame {
  uint64 request_id = 1;            // 客户端生成，响应原样带回
  oneof body {
    Auth auth = 2;                  // 必须是第一帧
    Ping ping = 3;
    message.v1.SendMessageRequest send_message = 4;
  }
}

message ServerFrame {
  uint64 request_id = 1;            // 对请求的响应时填写，推送为 0
  oneof body {
    AuthOK auth_ok = 2;
    Pong pong = 3;
    message.v1.SendMessageReply send_ack = 4;
    Error error = 5;                // code、reason、message、metadata，与 Kratos 错误一一对应
    message.v1.Message message = 6; // 新消息推送
    message.v1.ReadPosition read = 7;
    Kicked kicked = 8;              // 原因：退出、被踢、封禁等，随后关闭连接
  }
}
```

`SendMessageRequest` 包含：`peer_type`（单聊或群聊）、`peer_id`（对方用户 ID 或群 ID）、`client_msg_id`（1～64 位字母、数字、`-`、`_`）、`content`。

### 4.3 接口

对外（`message.v1.MessageService`，HTTP 与 gRPC，全部需要登录）：

| 方法 | HTTP |
|---|---|
| `ListConversations` | `GET /v1/conversations` |
| `GetConversation` | `GET /v1/conversations/{conversation_id}` |
| `ListMessages` | `GET /v1/conversations/{conversation_id}/messages` |
| `GetDifference` | `GET /v1/conversations/{conversation_id}/difference` |
| `MarkRead` | `POST /v1/conversations/{conversation_id}/read` |

内部（只有 gRPC，不经网关，同 TD-26）：

| 服务与方法 | 调用方 | 说明 |
|---|---|---|
| `message.v1.MessageInternalService.SendMessage` | access | 请求带发送者 ID、登录会话 ID、连接 ID（来自已鉴权的连接）以及 `SendMessageRequest` |
| `access.v1.AccessInternalService.Push` | delivery | 连接 ID 列表加 `ServerFrame` |
| `access.v1.AccessInternalService.Kick` | delivery | 连接 ID 列表加原因 |

**发送只走 WebSocket，不提供 HTTP 发送接口**（待确认）：只有一条写入路径，限流、排除发起连接、按连接顺序确认都只需实现一次。

### 4.4 事件（`api/message/v1/events.proto`，topic `marschat.message.events`，键为会话 ID）

| 事件 | 字段 |
|---|---|
| `MessageCreated` | `Message`、会话类型、单聊双方的用户 ID（群聊为空）、发起连接 ID |
| `ReadPositionUpdated` | 会话 ID、会话类型、用户 ID、`read_seq`、单聊对方 ID |

### 4.5 错误码

`message.v1.ErrorReason`：

| 原因 | HTTP |
|---|---|
| `CONVERSATION_NOT_FOUND` | 404 |
| `RATE_LIMITED` | 429（带 `retry_after_seconds`） |
| `DEPENDENCY_UNAVAILABLE` | 503 |

- 发言权限的拒绝原因直接沿用 social 的错误（`NOT_FRIENDS`、`BLOCKED`、`NOT_GROUP_MEMBER`、`MEMBER_MUTED`、`GROUP_MUTED`、`GROUP_DISMISSED`），不重新定义。
- 参数错误沿用 protovalidate 的 `400 VALIDATOR`。
- WebSocket 上的 `Error` 帧与 HTTP 错误使用同一套 code、reason 和 metadata。

## 5. 候选方案与权衡

| 问题 | 采用 | 未采用及原因 |
|---|---|---|
| 发消息入口 | WebSocket 帧 | HTTP `POST`：多一条写入路径，限流、排除发起端都要实现两遍 |
| WebSocket 鉴权 | 第一帧带令牌 | URL 参数：令牌会进入网关和代理的访问日志；借用 `Sec-WebSocket-Protocol` 头传令牌：语义不对，代理可能改写 |
| 连接失效 | 事件立即踢 + 每次心跳查吊销名单 | 只靠事件：Kafka 或 delivery 故障时，被封禁的连接一直在线；只靠心跳：最多延迟 30 秒 |
| 在线路由 | 用户级 Hash，按连接分字段，字段级 TTL | 整键 TTL（teamgram）：崩溃设备的字段会被其他设备的续期一直保留；每个连接一个键：查某个用户的全部连接要额外维护索引 |
| 路由粒度 | 连接 | 登录会话：同一浏览器的多个标签页共用登录会话 ID，会互相覆盖路由，排除发起端时还会把同一登录会话的其他标签页一起排除 |
| 推送方式 | delivery 按路由直连 access | 每个 access 实例都消费全部消息再自行过滤：流量随实例数成倍增长；Redis Pub/Sub：多一套通道，消息不落盘 |
| 群会话 ID | 等于群 ID | 单独的映射表：每条群消息多一次查询或缓存 |
| 单聊会话创建 | 第一条消息时按需创建 | 消费 `FriendAdded` 时创建：发送时仍要处理事件还没到的情况，两条路径做同一件事 |
| 群成员 | message 消费 social 事件维护一份 | 读接口每次调用 social：会话列表、历史、补拉都依赖 social 可用，并且每次多一次远程调用 |
| 新成员能否看历史 | 能看，但不计入未读 | 用入群时的 seq 隔断历史：入群事件处理之前发出的消息（包括新成员自己刚发的）会被隔断在外，需要额外的版本比对，见第 8 节 |
| 消息标识 | `(会话 ID, seq)` | 单独的 snowflake 消息 ID：多一个全局唯一索引，而且它不以分片键开头 |
| 幂等唯一键 | `(会话 ID, 发送者, client_msg_id)` | `(发送者, client_msg_id)`：不以分片键开头，分片后无法保证唯一 |
| 会话列表排序 | 按会话 ID 分页，客户端按最后消息时间排序 | 服务端按最近活跃排序：要么每条消息都更新每个成员的排序字段（这就成了写扩散），要么每次请求都对用户的全部会话排序，而且新消息会让分页结果跳动。重连时本来就要比对全部会话（ADR-0006），客户端拿到的是完整列表 |
| 正文存储 | protobuf 字节 | JSON：体积更大，类型约束靠约定；按类型分列：每加一种消息类型都要改表 |
| Outbox 多实例 | 每个服务一个租约，单实例发布 | `FOR UPDATE SKIP LOCKED`：同一个键的事件可能乱序，还要在持锁事务中调用 Kafka |
| 踢连接 | account 事件 → delivery → access `Kick` | access 直接消费 account 事件：每个 access 实例都要独立的消费组，还要处理大量与本实例无关的事件 |
| 按用户限制发消息频率 | message 用 Redis 统一计数；access 只做每连接帧频率保护 | 只在 access 限制：同一用户的多个连接可能落在不同实例，计数要放在 Redis；限制写在 message 里，以后增加任何入口也都能覆盖 |
| Kafka 客户端 | franz-go | segmentio/kafka-go：更新慢，消费组与提交的控制较粗；IBM/sarama：接口繁琐；confluent-kafka-go：依赖 cgo 和 librdkafka，Windows 下构建麻烦 |
| WebSocket 库 | gorilla/websocket | coder/websocket：接口更现代，但 gorilla 更成熟，而且已经在依赖图中（间接依赖） |

## 6. 新增依赖与环境变化

- **新增** `github.com/twmb/franz-go` v1.22.3（当前最新版）：只用核心包 `kgo`。`kadm`、`kfake` 是独立模块，不引入。
- **间接依赖改为直接依赖** `github.com/gorilla/websocket` v1.5.3（当前最新版，已在 `go.sum` 中）：access 服务端和 e2e 客户端都用它。
- compose：
  - 新增 `kafka-init`（创建 3 个 topic）和 `migrate-message`；
  - `marschat_message` 库和账号在步骤 1 已经建好。
- Makefile：新增 `run-access`、`run-message`、`run-delivery`；e2e 的说明改为需要启动 5 个服务。
- `buf.yaml`：枚举前缀例外加上 `api/message/v1/error_reason.proto`。
- 端口表：新增 access gRPC 19100。
- account、social 的配置新增 `kafka`、`outbox`，启动时多一个发布器；social 再多一个缓存消费者。
- Redis 要求不低于 7.4（Hash 字段级 TTL），当前为 8.8.4，写入 `deployment.md`。

## 7. 风险与验证

| 风险 | 应对 |
|---|---|
| franz-go 与 Kafka 4.4 的配合；步骤 1 也还没验证过外部监听器的收发 | 先写集成测试：经 19092 生产并在消费组中消费，验证提交与重启后续读，再写业务 |
| Kratos HTTP 请求 context 带超时，影响 WebSocket 长连接 | 实施 access 时先写“连接保持超过 1 秒”的测试 |
| 发布器租约在一批发布过程中过期，短时间内出现两个发布者，造成重复或乱序 | 续期间隔（3 秒）远小于租约（10 秒）；消费方幂等。乱序会影响群成员投影（极少发生），记入 TD，步骤 5 评估按事件 ID 加防护 |
| 处理器持续失败会阻塞分区（没有死信队列） | 记 ERROR；步骤 5 实现重试队列和死信队列 |
| 热点群的会话行锁成为写入瓶颈 | ADR-0006 已接受；步骤 8 压测时观察锁等待 |
| 成员投影依赖事件，有亚秒级延迟 | 写入发送时以 social 为准；读接口的延迟写入模块文档；e2e 中入群后等待成员投影生效 |
| 会话多时重连比对成本高 | ADR-0006 已接受，以后再评估批量补拉接口 |
| 新增的 `SendMessage`、`Push`、`Kick` 内部接口没有认证 | 扩大 TD-26 的范围 |

验证方案：

- **单元测试**：
  - message biz：用假 repo，覆盖发送各分支（幂等命中、并发重发、权限拒绝、social 不可用、限流）、补拉三种结果、已读只增不减、成员投影的事件处理；
  - access：用 `httptest` 加 gorilla 客户端，覆盖鉴权超时、鉴权失败、心跳超时、队列满时断开、帧频率超限、`Push` 与 `Kick`；
  - 发布器：用假的存储和生产者，覆盖批量、唤醒、失败退避、租约；
  - 在线路由：`HSETEX` 和字段 TTL 不确定 miniredis 是否支持，实施时确认，不支持就放到集成测试。
- **集成测试**（构建标签 `e2e`）：Kafka 收发与消费组提交；在线路由的字段 TTL。
- **e2e**（5 个服务一起运行，Go WebSocket 客户端）：
  - 单聊发送确认与对方实时收到；
  - 同一 `client_msg_id` 重发只存一条、返回同一 seq；
  - 多个协程并发向同一会话发送，seq 连续且没有重复；
  - 断线期间对方发消息，重连后 `GetDifference` 补齐，包括 `SLICE` 和 `TOO_LONG`；
  - 同一用户两台设备：发起设备收不到自己消息的推送，另一台能收到；
  - 已读后未读数归零，对方收到已读回执；
  - 群消息推给全体在线成员，被移除的成员不再收到；
  - 封禁或踢设备后连接被关闭；
  - 发言权限被拒绝时返回 social 的原因。
- **`EXPLAIN`**：发送、历史、补拉、会话列表、已读的查询，结果记在 `data-model.md`。

实施顺序（每段完成后跑一次完整验证）：

1. Kafka topic 初始化、`pkg/kafka`、发布器，在 account、social 中启用，加上 Kafka 集成测试；
2. message；
3. access；
4. delivery 与 social 缓存消费者；
5. e2e、`EXPLAIN`、文档。

## 8. 待开发者确认

以下几点超出计划原文或有多种合理做法，本文按推荐方案写入：

1. **依赖**：新增 franz-go v1.22.3；gorilla/websocket v1.5.3 由间接依赖改为直接依赖。两者都会修改 `go.mod`。
2. **发消息只走 WebSocket**，会话列表、历史、补拉、已读走 message 的 HTTP 接口。
3. **WebSocket 用第一帧鉴权**（不用 URL 参数）。连接不随访问令牌过期而断开，由踢连接事件和每次心跳的吊销检查保证失效。
4. **新入群成员能看到入群前的历史**，但历史不计入未读。另一种做法是像微信那样隔断历史：要么接受“入群事件处理之前发出的消息也被隔断”的边界情况，要么在 social 事件里加成员版本做比对。
5. **Outbox 每个服务用一个 Redis 租约，同时只有一个实例发布**，替代计划步骤 5 的 `FOR UPDATE SKIP LOCKED`。发布器同时在 account、social 中启用。
6. **群成员扩散提前到本步骤**（计划原属步骤 5）；social 事件的实时通知仍在步骤 5。
7. **TD-25 的 social 部分在本步骤处理**：social 消费自己的事件，重试删除群缓存。
8. **access 的 gRPC 端口用 19100**：19000 留给 MinIO。
9. **按用户限制发消息频率放在 message**（Redis 统一计数）；access 只做每连接的帧频率保护。计划原文写的是“access 按用户限制发送频率”。

## 9. 实施说明

2026-11-19 实施完成，与上文的差异和实施中的决定：

| 项 | 实施 | 原因 |
|---|---|---|
| 帧中的发送请求 | `ClientFrame.send_message` 用 `message.v1.OutgoingMessage`，确认用 `message.v1.SendResult`（4.2 节写的是 `SendMessageRequest`、`SendMessageReply`） | `SendMessageRequest` 是内部接口的请求，带发送者 ID、会话 ID、连接 ID，这些必须来自已认证的连接，不能让客户端填写 |
| `AuthOK` | 改名为 `AuthOk`，心跳间隔为 `heartbeat_interval_seconds`（整数秒） | 按 protobuf 的驼峰命名；整数比 `Duration` 在 JSON 子协议下更直观 |
| `Kicked` 的原因 | 复用 `account.v1.RevokeReason`；心跳发现吊销时为 `REVOKE_REASON_UNSPECIFIED` | 与吊销事件一一对应，不再定义一套 |
| access 的错误原因 | 新增 `access.v1.ErrorReason`：`INVALID_FRAME`（400）、`UPSTREAM_UNAVAILABLE`（503） | 设计没有定义 access 自身的错误；调用 message 失败时发送结果未知，要和 message 自己返回的 `DEPENDENCY_UNAVAILABLE` 区分 |
| 路由中的实例地址 | 新增配置 `advertise_addr`（默认 `127.0.0.1:19100`） | 取 Kratos gRPC 服务器的实际端点需要让 Hub 依赖 gRPC 服务器，而 gRPC 服务器又依赖 Hub（`Push`），wire 会成环；显式配置也与 Kafka 的 advertised listeners 做法一致，容器部署时需要填容器 IP |
| 在线路由的读写 | 抽成 `pkg/route`（`Register`、`Unregister`、`List`），access 和 delivery 共用 | 键与值的格式是两个服务的约定，放在一处 |
| Redis 版本要求 | 不低于 **8.0**（3.6 节和第 6 节写的是 7.4） | `HSETEX` 是 Redis 8.0 新增的命令，7.4 只有 `HEXPIRE` 一类。本地 8.8.4 不受影响；miniredis v2.40.0 支持 `HSETEX`，路由的字段 TTL 可以在单测中验证 |
| 心跳超时 | 直接断开 TCP，不发关闭帧（客户端看到 1006） | 对端多半已经失联 |
| 服务端主动关闭 | 写协程先发完队列中的帧和关闭帧，再等对端回应关闭帧，最多 2 秒，之后断开 TCP | 对端还有未读数据时直接关闭会触发 RST，`Kicked`、`Error` 帧可能丢失 |
| WebSocket 的 Origin | 不校验 | 认证靠首帧令牌而不是 Cookie，跨站页面拿不到令牌 |
| 上行帧频率 | 固定 1 秒窗口的进程内计数，认证帧也计入 | 只保护 access 自身，不需要精确 |
| Redis 写路由失败 | 只记 WARN，连接保持 | 连接仍能收发和补拉，下一次心跳会重新登记；断开会让全部客户端在 Redis 故障时反复重连 |
| delivery 去重与取成员的顺序 | 先 `SET NX` 占位，再取群成员；取成员失败时这条事件不再推送 | 与“先占位后推送，宁可漏推”一致，漏掉的靠补拉 |
| 各消费组的起始位置 | `marschat-message` 从最早开始（不漏掉 message 首次启动前建的群）；`marschat-delivery`、`marschat-social-cache` 从最新开始 | 推送只对在线连接有意义；缓存删除在事件之前已经同步做过一次 |
| social 缓存消费者处理的事件 | `GroupCreated`、`GroupUpdated`、`GroupDismissed`、`GroupMembersJoined`、`GroupMemberLeft`、`GroupMemberUpdated` | 凡是改变群资料或成员列表的事件都删一次；入群申请不改变两者 |
| social 的缓存删除接口 | 新增 `biz.GroupCacheInvalidator`，一次 `DEL` 两个键并返回错误 | 原有的 `EvictCache` 吞掉错误，消费者需要错误来重试 |
| Outbox 记录的时间戳 | 不设置 `Record.Timestamp` | franz-go 按记录时间戳计算投递超时，积压的旧事件会立即判为超时，见 [lessons/franz-go-record-timestamp-timeout.md](../lessons/franz-go-record-timestamp-timeout.md) |
| 死锁重试 | 未实现，记为 TD-33 | 识别 MySQL 1213 需要直接依赖驱动的错误类型（改 `go.mod`）；按设计不会死锁，并发 e2e 中也没有出现 |
| 不使用 `FriendAdded` | 单聊会话在第一条消息时创建（3.4 节已说明） | social 设计 5 节写的“message 消费 `FriendAdded`”不再需要 |

实施中发现的问题：

- franz-go 的记录投递超时从 `Record.Timestamp` 开始计算，见上表和 lesson。
- 设计中 Redis 最低版本写错（见上表）。
- 新增 TD-30（没有重试队列和死信队列）、TD-31（delivery 缓存的 access 连接不摘除）、TD-32（Outbox 租约过期时可能短暂出现两个发布者）、TD-33（发消息不重试死锁）；TD-26 扩大到 `MessageInternalService`、`AccessInternalService`；TD-25 的 social 部分已解决，只剩 account，优先级调回低。

验证结果：

- 单元测试（`go test -race ./...`，24 个包通过）：
  - `pkg/kafka`：按顺序处理与提交、原地重试到成功、`Permanent` 与 panic 跳过、未注册类型跳过、停止时取消、`On[T]` 解码；
  - `pkg/outbox`：分批与顺序、没拿到租约不发布、失败退避与上限、租约丢失停止、唤醒与停止时释放租约、清理；
  - `pkg/auth`：每个令牌用例同时经过中间件和 `Verifier` 并比对结论，另测心跳复查吊销；
  - `pkg/route`：登记、查询、删除，字段级 TTL（一条连接停止续期后单独过期），无法解析的字段被跳过；
  - message biz：发送各分支（幂等、并发重发、权限拒绝、social 不可用、限流）、补拉三种结果、已读只增不减、成员投影。把 seq 分配故意改坏后 5 个用例失败，确认测试有效；
  - access（`httptest` 加真实 gorilla 客户端）：认证成功与续期、JSON 子协议、首帧不是认证、令牌无效、认证超时、心跳超时、发送确认与错误透传、同一连接内按顺序确认、无法解码的帧、推送与踢连接、心跳发现吊销、Redis 故障放行、帧频率超限、超大帧、慢连接、停机关闭全部连接；另有一个测试确认 WebSocket 连接不受 Kratos HTTP 请求超时影响。去掉心跳续期路由后对应用例失败；
  - delivery：单聊排除发起连接、按实例分组、重复事件只推一次、去重失败不推、群成员扩散与取成员失败、已读推给本人和对方、按会话踢、封禁踢全部、部分路由损坏、超过 1000 个连接分批。
- e2e（`make e2e`，五个服务同时运行，连续两次 30 项全部通过）：
  - Kafka 经宿主机端口收发、按处理进度提交、重启后续读；account、social 的 Outbox 事件到达 Kafka；
  - message：单聊发送、重发去重、未读与已读、历史、`SLICE` 补拉、已读回执、非好友拒绝；两人并发发送 seq 连续无重复；群会话随建群、入群、移除变化；
  - access：经 WebSocket 发送与确认、重发返回同一 seq、`NOT_FRIENDS` 与 `VALIDATOR` 透传、在线路由的写入与删除、JSON 子协议、令牌无效 4001、退出登录后被踢 4002；
  - delivery：发起连接只收到确认、同一用户的另一条连接和对方收到推送、已读推送到三条连接；群消息推给全体在线成员，被移除的成员收不到；封禁后经事件立即踢线（原因 `BANNED`）；
  - social 缓存：投递一条 `GroupUpdated` 后，残留的群资料和成员列表缓存被删除。
  - 未覆盖：`TOO_LONG` 只有单元测试（e2e 中要发 1000 条以上消息，与每人每 10 秒 30 条的限流冲突）；跨 access 实例推送要等步骤 5 多实例部署后验证。
- `EXPLAIN`：在临时库中造数据后核对，所有关键查询都走预期索引、没有排序，结果记录在 [data-model.md](../architecture/data-model.md#23-messagemarschat_message)。
- 其他：`buf lint`、`make gen` 后生成代码不变、`go mod tidy -diff`、`go vet`（含 e2e 标签）、`go build` 均通过；各服务日志在 e2e 期间没有 ERROR。
