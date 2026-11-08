# 技术债与已知问题

> 2026-11-07 首次提交时通过**静态阅读代码**整理，除注明“已验证”外均未在运行环境中复现。
> 修复某项后，把它移到文末“已解决”并注明提交；发现新问题时按同一格式追加，编号不复用。

优先级：**高**＝安全风险或可导致服务不可用；**中**＝功能错误或妨碍扩展；**低**＝一致性、死代码与体验问题。

## 高

| 编号 | 问题 | 位置 | 建议 |
|---|---|---|---|
| TD-01 | **没有认证与鉴权**。HTTP 接口信任请求体中的 `owner_id`、`uuid`；WebSocket 只凭 `client_id` 参数识别用户，任何人都能以他人身份连接并接收其消息；管理接口只由前端按 `is_admin` 隐藏入口，服务端不校验 | `internal/https_server/https_server.go`、`api/v1/ws_controller.go` | 登录后签发令牌，HTTP 中间件和 WS 握手统一校验；管理接口服务端校验管理员身份 |
| TD-02 | **密码明文存储与比较**，字段为 `char(18)` | `user_info_service.go` 的 `Login`、`Register`；`model/user_info.go` | 改用 bcrypt 等慢哈希，调整字段长度并迁移存量数据 |
| TD-03 | **部署路径与地址硬编码**：配置文件路径、MySQL 走 Unix Socket 且用 `appName` 作库名（忽略 `mysqlConfig` 的 password/host/port/databaseName）、TLS 证书路径、前端 `backendUrl`/`wsUrl` 写死为公网 IP、`vue.config.js` 证书与 443 端口 | 见 [deployment.md](../deployment.md#硬编码项清单) | 配置路径改为命令行参数或环境变量；DSN 使用完整配置；前端地址改为 `.env` |
| TD-04 | **单条异常消息可使后端崩溃**。channel 模式的转发循环没有 `recover`：`normalizePath` 在头像不含 `/static/` 时执行 `path[-1:]` 越界；`receive_id` 为空时 `ReceiveId[0]` 越界；发送方不在在线表时对 nil 指针取字段。kafka 模式虽有 `recover`，但读取协程随之退出，之后静默停止消费 | `internal/service/chat/server.go`、`kafka_server.go` | 入口处校验消息字段；单条消息的处理包一层 `recover` 并记录日志，不影响循环继续 |

## 中

| 编号 | 问题 | 位置 | 建议 |
|---|---|---|---|
| TD-05 | kafka 模式下登出无效：`ClientLogout` 固定在 `ChatServer.Clients` 中查找，而该模式的连接登记在 `KafkaChatServer.Clients`；退出时无论模式都调用 `ChatServer.Close()` | `service/chat/client.go`、`cmd/mars_chat_server/main.go` | 两种模式抽象出同一接口，按模式调用 |
| TD-06 | 关停流程不安全：`Close()` 关闭通道后，`Start` 循环会从已关闭通道读到 nil 并解引用；退出时 `DeleteAllRedisKeys` 清空当前 Redis 库的全部键（含验证码及同库其他数据）；不等待在途消息 | `main.go`、`server.go`、`redis_service.go` | 用 context 控制退出；只删除本服务前缀的键或干脆不删 |
| TD-07 | 只能单实例运行：在线连接表在进程内存；kafka 模式多实例时消费组会分摊消息，接收方可能连在另一实例上导致丢投；Writer 为 `RequireNone`（不等确认）、未自动建 topic，`CreateTopic` 还把 `partition`(0) 当作分区数 | `service/chat/`、`service/kafka/kafka_service.go` | 如需水平扩展，引入在线路由（如 Redis 记录用户所在实例）或按实例广播后过滤 |
| TD-08 | WebSocket 连接生命周期管理缺失：`Read` 出错直接返回，不从在线表移除、不关闭通道；同一用户重连会覆盖在线表条目，旧协程泄漏；向已断开连接的 `SendBack` 写满 100 条后，转发循环会在持锁状态下阻塞，进而阻塞全部转发；`ClientLogout` 先投递 Logout 再关闭 `SendBack`，与转发循环存在竞态，可能 `send on closed channel` | `service/chat/client.go`、`server.go` | 断开时统一注销；向 `SendBack` 写入改为非阻塞或带超时；关闭通道由唯一所有者负责 |
| TD-09 | Redis 缓存实现不完整：消息列表与详情类缓存只读不写（读取分支总是回源，追加分支不触发）；`LeaveGroup` 键名多一个空格，`EnterGroupDirectly` 与通过加群申请用群 ID 拼用户键，失效均无效；多处只清理操作者一方的缓存，依赖 1 分钟 TTL 兜底 | `service/gorm/*.go`，详见 [data-model.md](../architecture/data-model.md#4-redis-键) | 先决定哪些数据值得缓存，再成对补齐“写入 + 失效”，或移除无效代码 |
| TD-10 | 文件上传：按原文件名保存，同名覆盖；服务端不限制大小与类型（`ParseMultipartForm` 的参数是内存阈值，不是上限，前端限制 50MB）；`/static` 无鉴权公开；文件消息 `url` 存带服务器地址的绝对 URL，与头像的相对路径约定不一致 | `service/gorm/message_service.go`、`ContactChat.vue` | 服务端生成唯一文件名并限制大小类型；`url` 改存相对路径 |
| TD-11 | 音视频：`ICE_CFG` 为空，跨 NAT 或公网通话无法建立；群聊页面也显示通话按钮，但后端只转发 `U` 开头的接收方，群聊通话不可用 | `ContactChat.vue`、`service/chat/server.go` | 配置 STUN/TURN（示例见 [deployment.md](../deployment.md#turn-与-ice-配置)）；群聊隐藏通话按钮或实现多人方案 |
| TD-12 | 没有可运行的自动化测试：`test/dao` 引用已不存在的字段，编译失败（已验证：`go vet ./test/...`）；其余测试通过 `config.GetConfig` 依赖固定路径的配置文件和真实 MySQL/Redis | `test/` | 先修复编译；业务逻辑抽出可注入依赖的部分再补单元测试 |
| TD-13 | 短信：验证码用 `fmt.Println` 打印到标准输出；签名与模板编码写死在代码里，`authCodeConfig.signName/templateCode` 未使用；除“1 分钟内不重发”外没有防刷 | `service/sms/auth_code_service.go` | 去掉明文打印；使用配置项；按手机号和 IP 限频 |
| TD-14 | `server.go` 与 `kafka_server.go` 重复约 400 行消息处理逻辑，文本与文件分支之间也互相复制，修改时容易漏改一处 | `service/chat/` | 抽取“落库 + 计算接收方 + 推送”的公共函数 |

## 低

| 编号 | 问题 | 位置 |
|---|---|---|
| TD-15 | `SetAdmin` 用 `uuid = (?)` 传入列表，多选时生成的 SQL 预计报错（未验证） | `user_info_service.go` |
| TD-16 | 群组逻辑：`DismissGroup`、`DeleteGroups` 查询申请记录时错误判断写反；解散群不把 `status` 置为 2；`RemoveGroupMembers` 即使成员不在列表中也会减少 `member_cnt`；群主处理加群申请时服务端不校验群主身份 | `group_info_service.go`、`user_contact_service.go` |
| TD-17 | 申请逻辑：加群申请被拒后再次申请不会重置为“申请中”，群主看不到；已通过的好友申请再次提交会被改回“申请中” | `user_contact_service.go` 的 `ApplyContact` |
| TD-18 | 前端：`App.vue` 的 `logout` 引用未定义的 `data`、`router`、`ElMessage`，被禁用用户刷新页面时自动登出会抛错；联系人搜索框没有过滤逻辑；三个登录页各自重复创建 WebSocket | `web/chat-server/src/` |
| TD-19 | `user_contact.status` 的 model 注释与枚举顺序相反（注释 1 拉黑、2 被拉黑、3 删除、4 被删除），实际写入与枚举一致 | `model/user_contact.go` |
| TD-20 | 死代码与未用字段：chatroom 接口（`chatRooms` 从不写入，恒返回空，前端未调用）；前端处理的 `CURRENT_PEERS`/`PEER_JOIN` 后端从不发送；`aes` 包未被引用；`loginTopic`/`logoutTopic` 未使用；语音消息类型无处理；`session.last_message*`、`message.send_at`、`user_info.last_online_at/last_offline_at` 从不写入 | 多处 |
| TD-21 | 日志：引入了 lumberjack 但 `getFileLogWriter` 未使用，日志文件不切割；`OpenFile` 权限写成十进制 `644`；`log.Println` 与 `zlog` 混用，并打印完整消息内容 | `pkg/zlog/logger.go` 等 |
| TD-22 | 其他：首个管理员只能手动改库（注册时 `is_admin` 恒为 0）；`TlsHandler` 在校验出错时调用 `zlog.Fatal` 直接终止进程（例如被反向代理以 HTTP 转发时）；CORS 与 WebSocket `CheckOrigin` 放行所有来源；ID 用日期加随机数生成，冲突时依赖唯一索引报错；历史消息一次返回全部，无分页；`message.status=1` 也会因回显给发送方而被设置，不代表对方已收到 | 多处 |

## 已解决

| 编号 | 处理 | 提交 |
|---|---|---|
| — | go.mod 把直接依赖 `gin-contrib/cors`、`segmentio/kafka-go`、`unrolled/secure` 标为 `// indirect`：执行 `go mod tidy` 后已修正 | 首次提交 |
