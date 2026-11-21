# 技术债与已知问题

> 记录重构后代码中的技术债与已知问题。修复某项后，把它移到文末“已解决”并注明提交；发现新问题时按同一格式追加，编号不复用。
>
> 编号从 TD-24 起。TD-01 至 TD-23 属于已移除的旧基线，见提交 `6624cea` 及之前的版本。

优先级：**高**＝安全风险或可导致服务不可用；**中**＝功能错误或妨碍扩展；**低**＝一致性、死代码与体验问题。

## 高

暂无。

## 中

### TD-42 delivery 全部停止后没有告警

- 发现：2026-11-21，重构步骤 8 的积压告警实验
- 位置：`pkg/kafka/lag.go` 的 `LagMonitor`（运行在 delivery 中）；`deploy/prometheus/rules.yml`
- 现状：Kafka 积压由 delivery 用 kadm 统计。两个 delivery 实例都停止时，`marschat_kafka_consumer_lag` 随之消失，`KafkaConsumerLag` 不会触发，而这正是推送全部中断、积压最快增长的时候。`InstanceDown` 能覆盖前 10 分钟：被杀的实例在 Consul 中保持 critical，Prometheus 仍抓取并得到 `up == 0`；600 秒后实例注销，抓取目标消失，告警随之结束，之后没有任何告警。其他服务全部实例停止时同样如此。
- 处理：加一条按服务检查的“没有存活实例”告警（例如对每个服务 `absent(up{job="marschat", service="delivery"} == 1)`，持续 1 分钟）；或者把积压统计移出 delivery（kafka-exporter，或其他常驻服务），让它不随消费者一起消失。

### TD-26 服务间 gRPC 调用没有认证

- 发现：2026-11-18，重构步骤 2；步骤 3 新增 `SocialInternalService`；步骤 4 新增 `MessageInternalService`、`AccessInternalService`
- 位置：各服务 `api/<服务>/v1/internal.proto`，以及 `internal/server/server.go` 的 `requiresAuth`（access 的 gRPC 服务器不挂令牌校验）
- 现状：内部接口只注册在 gRPC 上、不经过令牌校验，安全性依赖服务网络不对外暴露。服务容器化（步骤 5）后，同一网络中的任意容器都能调用，例如读取任意群的成员列表、探测任意两人是否为好友；步骤 4 之后还能以任意用户的身份发消息（`SendMessage` 的发送者取自请求），或向任意连接推送帧、踢掉任意连接（`Push`、`Kick`）。
- 处理：步骤 5 起评估 mTLS 或服务令牌。

## 低

### TD-25 account 的资料缓存删除失败时最长有 30 分钟旧数据

- 发现：2026-11-18，重构步骤 2；步骤 3 扩大到 social，优先级调为中；步骤 4 解决 social 部分后调回低
- 位置：`app/account/internal/data/user.go` 的 `EvictCache`
- 现状：写操作在数据库提交之后删除缓存，失败只记 WARN，靠 TTL（30 分钟 ±10%）兜底，影响是资料短时间不一致。social 的群资料与成员列表已在步骤 4 改为经事件兜底删除（消费组 `marschat-social-cache`，Redis 故障时重试到成功），被移除或被禁言的成员不会再因删除失败而继续发言。
- 处理：account 的资料修改目前没有对应事件。需要时新增 `UserProfileUpdated` 事件，按 social 的方式兜底。

### TD-32 Outbox 旧持有者卡住超过 5 秒时，迟到的重复事件可能让群成员投影回退

- 发现：2026-11-19，重构步骤 4；步骤 5 核对后缩小范围、改写标题
- 位置：`pkg/outbox/publisher.go`；`app/message/internal/biz/membership.go`
- 现状：租约 10 秒、每 3 秒续期，每批发布前要求本地认定的剩余租期不少于一次发布超时（5 秒），不足时先续期，续不上就停止发布（步骤 4 已实现，原先描述的“续期之间出现两个发布者”在正常调度下不会发生）。剩下的情况是持有者在一批发布途中整个进程停顿超过 5 秒（长 GC 停顿、虚拟机冻结）：新持有者拿到租约后从未标记的事件开始按 ID 顺序重新发布，旧持有者恢复后已经交给 Kafka 客户端的记录可能晚于新持有者的后续事件到达。每个事件第一次出现时仍然有序，问题只在迟到的重复：大多数消费者按事件 ID 去重，不受影响；message 的群成员投影没有按事件 ID 去重，只依赖“重放一段前缀结果不变”，迟到的入群事件落在退群之后会把已退群的成员加回来。
- 处理：在 Kafka 记录头中带上租约代数（获取租约时自增），群成员投影按分区记住见过的最大代数，丢弃更旧代数的记录；或者投影按事件 ID 去重。触发条件需要进程停顿超过 5 秒，暂不处理。

### TD-33 发消息遇到数据库死锁时直接返回 500

- 发现：2026-11-19，重构步骤 4
- 位置：`app/message/internal/biz/message.go` 的 `Send`
- 现状：分配 seq 的事务锁会话行，按设计不会死锁（会话创建用 `ON DUPLICATE KEY UPDATE` 避免 `INSERT IGNORE` 的锁升级，存在性检查放在事务外）。但如果出现 MySQL 1213，错误不会被识别和重试，客户端收到 `500 INTERNAL`，需要用同一个 `client_msg_id` 重发。识别 1213 需要直接依赖 MySQL 驱动的错误类型，涉及 `go.mod`，暂未处理。并发 e2e 中没有出现过死锁。
- 处理：压测（步骤 5）中观察到死锁时，在事务层对 1213 重试一次。
- 2026-11-21 步骤 8 压测：1000 个连接、约每秒 1000 条单聊消息持续 60 秒，message 没有出现死锁。同类问题在 account 的注册中出现过（TD-40），登录还有残留风险（TD-43）。

### TD-43 并发登录仍可能在会话唯一索引的间隙上死锁

- 发现：2026-11-21，重构步骤 8 修复 TD-40 时分析
- 位置：`app/account/internal/biz/auth.go` 的 `startSession`；`app/account/internal/data/session.go` 的 `RevokeActiveByDevice`
- 现状：登录时用 `SELECT ... WHERE user_id = ? AND active_device = ? FOR UPDATE` 锁住该设备上的旧会话，查不到行时加的是间隙锁。两个用户的 ID 在索引中相邻、又同时用新设备登录时，两人要插入的键可能落在同一个间隙（前一个用户最后一条会话记录与后一个用户第一条会话记录之间），各自持有间隙锁后再插入就会死锁，其中一个返回 `500 INTERNAL`。注册已经不做这次查询（TD-40）；老用户都有会话记录，把索引分成各自的小间隙，并发登录撞上的概率很低，压测中没有出现。
- 处理：与 TD-33 一起，在事务层识别 1213 并重试整个事务一次；或者登录改为先插入、靠唯一约束兜底（冲突时再吊销旧会话后重试）。

### TD-28 布隆过滤器加入失败的重试期间会把新 ID 判为不存在

- 发现：2026-11-19，重构步骤 3
- 位置：`pkg/bloom/bloom.go` 的 `Add`
- 现状：`BF.INSERT` 失败后后台按 1s、2s、4s 重试，重试耗尽才删除就绪标记、触发重建。重试期间过滤器仍是就绪状态且不含新 ID，查询这个新用户或新群会返回“不存在”，并写入 1 分钟的空值缓存。只在 Redis 能读写、但 `BF.INSERT` 单独失败时出现（例如过滤器达到内存上限）。
- 处理：第一次加入失败时立即删除就绪标记（查询改为放行），重试成功后由下一次查询触发重建；或在建群、注册成功后主动删除该 ID 的空值缓存。改动会影响 account 的行为，留到有压测数据后再定。

### TD-37 上传过程中进程退出可能留下没有记录的孤儿对象

- 发现：2026-11-20，重构步骤 6
- 位置：`app/media/internal/biz/file.go` 的 `Upload`
- 现状：上传先写存储、再插入 `files` 记录；插入失败时会尽力删除刚写入的原文件和缩略图，但进程在两步之间退出（被杀、崩溃）时对象留在存储中，没有记录指向它。只占用空间，不影响正确性：没有记录就换不到下载地址。
- 处理：做文件清理时一并处理，例如定期列出存储中超过一天、且在 `files` 表中查不到的对象并删除（OSS 也可以用生命周期规则配合标签）。

### TD-38 缩略图不处理 JPEG 的 EXIF 方向

- 发现：2026-11-20，重构步骤 6
- 位置：`app/media/internal/biz/image.go` 的 `Thumbnailer.Make`
- 现状：标准库解码 JPEG 时忽略 EXIF Orientation，部分手机竖拍的照片生成的缩略图是横着的，记录的宽高也是旋转前的。原图不受影响，浏览器显示原图时会按 EXIF 旋转。
- 处理：读取 EXIF 方向（需要新的依赖或自己解析 APP1 段），解码后按方向旋转，再记录宽高。

## 已解决

### TD-41 各服务连接池上限之和超过 MySQL 最大连接数

- 发现：2026-11-21，重构步骤 8 压测 1000 个连接，0.25% 的发送失败，日志 `Error 1040: Too many connections`
- 解决：2026-11-21，重构步骤 8
- 原因：所有服务共用一个 MySQL，容器模式下各实例 `max_open_conns` 之和为 230（account 50、social 50、message 50 × 2、media 30），MySQL 默认最大连接数 151，峰值用到 152 条时新连接被拒绝。
- 处理：先尝试把连接池缩小到合计 120，结果 message 每实例 30 条连接不够用，请求排队到超时，access 的熔断器打开，失败反而达到 2.7 万次。最终保留原来的连接池，MySQL 最大连接数调到 300（`compose.yaml` 的启动参数），再测峰值用到 158 条，没有错误。连接预算写进 [deployment.md](../deployment.md#容器模式)，增加实例或调大连接池时要同步核对。数据见[重构计划的压测结果](active/backend-refactor.md#压测结果)。

### TD-40 并发注册在会话唯一索引的间隙上死锁

- 发现：2026-11-21，重构步骤 8 压测准备数据，4 个并发注册有 3 个返回 `500 INTERNAL`（`Error 1213`）
- 解决：2026-11-21，重构步骤 8
- 原因：注册事务调用 `startSession`，先用 `SELECT ... FOR UPDATE` 锁住设备上的旧会话。新用户查不到行，InnoDB 加的是间隙锁；新用户 ID 递增，都落在 `uk_user_active_device` 末尾的同一个间隙，各自持有间隙锁后插入会话时互相等待。
- 处理：`startSession` 拆出 `createSession`，注册直接调用它，不做加锁查询（新用户不可能有会话）。e2e 用例 `TestConcurrentRegister` 同时发 8 个注册请求；恢复旧代码时其中 2 个死锁，修复后全部成功。修复后 k6 并发注册 1000 个用户没有失败。登录仍有低概率的同类风险，见 TD-43。经过见 [lessons/innodb-gap-lock-register-deadlock.md](../lessons/innodb-gap-lock-register-deadlock.md)。

### TD-39 Redis 短暂停机后，节点号租约的下一轮续期可能晚于本地截止时间

- 发现：2026-11-21，重构步骤 6 验收时的 `make cluster-test`。`TestFaultStopRedisLong` 在停 Redis 之前的准备阶段发好友申请，收到 `503 DEPENDENCY_UNAVAILABLE`；它紧跟在停 Redis 20 秒的实验之后。
- 解决：2026-11-21，重构步骤 6
- 原因：`pkg/idgen` 每 10 秒续期一次，租约 TTL 为 30 秒。续期失败时，只要本地截止时间还没到，下一轮就要等满 10 秒。Redis 停机 20 秒左右时，两次续期失败，第三次正好撞上截止时间：social 日志里 09:12:05.06 发号失败，80 毫秒后续期发现键已过期，换了新节点号。Redis 早已恢复，中间却有一小段发号失败的窗口。之前的运行能通过，只是时间相位碰巧错开了。
- 处理：`renew` 返回是否持有有效租约，续期失败后下一轮按 `retryEvery`（1 秒）重试，续上之后恢复 `renewEvery`。只要停机比 TTL 短 1 秒以上，Redis 恢复后都能在截止时间前续上，节点号不变。单元测试见 `pkg/idgen` 的 `TestRenewRetriesSoonAfterError`。代价：Redis 不可达期间，每个实例每秒多一次续期尝试和一行 WARN 日志。

### TD-24 短信只有写日志的发送方

- 发现：2026-11-18，重构步骤 2
- 解决：2026-11-20，插入任务“邮箱验证码”（不再适用）
- 处理：个人认证的阿里云账号无法申请短信签名，开发者决定账号标识改为邮箱。验证码经 SMTP 发到邮箱（`app/account/internal/data/mail.go`，标准库实现，465 端口隐式 TLS、其他端口要求 STARTTLS，截止时间不超过请求剩余时间，拒绝发往保留域名），`log` 发送方保留给开发与测试。发送失败时撤销发送间隔和每日计数、删除验证码，返回 `503 CODE_SEND_FAILED`，原有逻辑由单元测试覆盖；SMTP 发送方由假 SMTP 服务器的单元测试覆盖隐式 TLS、STARTTLS、拒绝明文、认证失败、证书不受信任、超时。设计见 [email-verification.md](../design/email-verification.md)。

### TD-35 节点号租约失效期间，需要发号的写操作返回 500 并记 ERROR

- 发现：2026-11-20，重构步骤 5 的 Redis 故障实验
- 解决：2026-11-20，插入任务“邮箱验证码”
- 处理：account、social、message 的 biz 层新增 `nextID`，把 `idgen.ErrLeaseLost` 转成 `503 DEPENDENCY_UNAVAILABLE`（account 新增该错误原因，值 60），其他发号错误仍按内部错误处理；`pkg/middleware/accesslog` 改为 503 记 WARN、其他 5xx 记 ERROR（原文写的“访问日志按 503 记 WARN”与代码不符，当时所有 5xx 都记 ERROR）。account 注册改为先发号再校验验证码，租约失效不消耗验证码。单元测试见三个服务的 `TestIDLeaseLostReturnsUnavailable`、`TestSendIDLeaseLost` 与 `accesslog` 的 `TestServerErrorLevels`。

### TD-36 go-redis 内部日志是纯文本，Redis 不可达期间每次拨号失败都打一行

- 发现：2026-11-20，重构步骤 5 的 Redis 故障实验
- 解决：2026-11-20，插入任务“邮箱验证码”
- 处理：`bootstrap.NewRedis` 用 `redis.SetLogger` 把 go-redis 的内部日志转到 `slog`（`msg="go-redis internal log"`，原文在 `detail`）。go-redis 不给级别，按内容分级：连接池拨号失败记 DEBUG，内部恢复的 panic 记 ERROR，其他记 WARN。单元测试见 `pkg/bootstrap/redis_test.go` 的 `TestRedisLoggerLevels`，`TestNewRedisRecoversAfterRestart` 验证真实的拨号失败经 `slog` 输出。

### TD-29 拉黑与好友申请并发时可能留下一条待处理申请

- 发现：2026-11-19，重构步骤 3
- 解决：2026-11-20，插入任务“邮箱验证码”
- 处理：同意好友申请时在同一事务内再检查一次拉黑：任一方已拉黑就把申请改为已失效（与拉黑时的处理一致），提交后返回 `403 FRIEND_REQUEST_NOT_ALLOWED`，不建立好友关系。收到的申请列表仍会显示这条申请，直到接收方处理它。单元测试见 `app/social/internal/biz/friend_test.go` 的 `TestAcceptRequestFromBlockedUser`。

### TD-30 Kafka 消费没有重试队列和死信队列

- 发现：2026-11-19，重构步骤 4
- 解决：2026-11-20，重构步骤 5
- 处理：`pkg/kafka` 加入共用的死信队列 `marschat.dlq`（3 分区，保留 14 天）：`Permanent` 错误、解码失败、panic 的记录带上消费组、原位置和失败原因写入后再提交位点，写入失败时不提交、退避重试。`ConsumerConfig.RetryTopic` 为不要求顺序的消费者启用重试队列：原地尝试 3 次仍失败就转入重试 topic 并提交原位点，重试消费者（消费组加 `-retry`）10 秒后再处理，临时错误原地无限重试，只阻塞重试 topic。目前只有 `marschat-social-cache` 使用（`marschat.social-cache.retry`）。要求顺序的 `marschat-message` 仍原地无限重试，连续失败 10 次后改记 ERROR 并限频。单元测试见 `pkg/kafka/consumer_test.go`，`test/e2e/kafka_test.go` 经真实 Kafka 验证重试队列往返与死信记录。死信积压告警留到步骤 8 的监控。

### TD-34 节点号租约丢失后不会重新获取

- 发现：2026-11-19，重构步骤 5 设计时核对代码
- 解决：2026-11-19，重构步骤 5
- 处理：续期脚本返回 0（键已过期或被其他实例占用）时立即停止用旧节点号发号，再获取一个空闲节点号，记 WARN；获取失败时每秒重试，Redis 故障期间续期出错也按每秒重试（2026-11-21 核对：当时的代码只在本地租约失效后才每秒重试，续期出错但租约未到期时仍等满 10 秒，见 TD-39，已修正）。换用新节点号而不是等旧租约到期：新节点号上的 ID 不会与旧节点号冲突。`pkg/idgen/idgen_test.go` 覆盖键被占用、Redis 中键过期后重新获取、续期出错时在本地截止时间前继续使用旧节点号。Redis 故障实验在步骤 5 的故障实验阶段验证。

### TD-31 delivery 缓存的 access 连接不会摘除

- 发现：2026-11-19，重构步骤 4
- 解决：2026-11-19，重构步骤 5
- 处理：`pusher` 调用返回 `Unavailable` 或 `DeadlineExceeded` 时关闭并移出该地址的连接，地址进入冷却期（新增热更新配置 `policy.push_cooldown`，默认 5 秒），冷却期内的推送直接跳过，不再每条事件都等一次超时；冷却结束后重新拨号，过期的冷却记录随下一次摘除清理。业务错误不摘除。没有改为监听 Consul：正在排空的 access 已从 Consul 注销，但还要继续给剩余连接推送。单元测试见 `app/delivery/internal/data/pusher_test.go`。

### TD-27 布隆过滤器的失败分支没有自动化测试

- 发现：2026-11-18，重构步骤 2
- 解决：2026-11-19，重构步骤 3（与步骤 3 一起提交）
- 处理：过滤器抽到 `pkg/bloom`，`test/e2e/bloom_test.go` 连真实 Redis 覆盖重建与查询、加入重试成功、重试耗尽后重建、重建期间变脏再来一轮、始终变脏时保持未就绪、别的实例持锁时跳过、Redis 不可用时放行。
- 测试补上后立即发现一个缺陷：`BF.EXISTS` 在 RESP3 下返回布尔值，原代码按整数读取，读取总是失败并放行，步骤 2 以来过滤器实际没有拦截过任何 ID。已改为 `Cmd.Bool()`，解析失败时记 WARN。见 [lessons/redis-resp3-reply-types.md](../lessons/redis-resp3-reply-types.md)。
