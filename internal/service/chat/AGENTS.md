# internal/service/chat 开发约束

本目录负责 WebSocket 连接管理和聊天消息转发。整体机制见 [docs/architecture/data-flow.md](../../../docs/architecture/data-flow.md)，已知问题见 [tech-debt.md](../../../docs/plans/tech-debt.md) 的 TD-04、TD-05、TD-07、TD-08、TD-14。

## 文件分工

| 文件 | 内容 |
|---|---|
| `client.go` | `Client` 结构、`Read`/`Write` 协程、`NewClientInit`、`ClientLogout` |
| `server.go` | channel 模式的 `ChatServer` |
| `kafka_server.go` | kafka 模式的 `KafkaChatServer` |

## 修改时必须注意

1. **两份实现同步修改**：消息处理逻辑在 `server.go` 和 `kafka_server.go` 中各有一份，文本与文件分支之间也是复制的。改一处要检查其余几处，除非本次就是在消除这份重复。
2. **按运行模式选择服务**：`client.go` 中凡是访问在线表或投递登录、登出的地方，都要区分 `ChatServer` 和 `KafkaChatServer`（`ClientLogout` 目前没有区分，见 TD-05）。
3. **转发循环不能阻塞**：转发是单协程串行处理，并在持有 `mutex` 时写入各连接的 `SendBack` 通道。不要在持锁期间加入可能长时间阻塞的操作。
4. **不要让单条消息引发 panic**：channel 模式的循环没有 `recover`，任何越界或空指针都会使整个进程退出。处理客户端上送的字段前先校验（例如 `receive_id` 非空、头像路径格式）。
5. **通话信令只发给对方**：`type=3` 的消息不回显给发送方，原因见 [lessons/av-signaling-no-echo.md](../../../docs/lessons/av-signaling-no-echo.md)。
6. **协议变化要三处同步**：消息字段或信令类型变化时，同时修改前端 `web/chat-server/src/views/chat/contact/ContactChat.vue` 和 `data-flow.md`。
