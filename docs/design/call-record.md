# 设计：通话记录写入聊天

- 状态：已采纳（2026-11-23） | 日期：2026-11-22 | 关联：[call-signaling.md](call-signaling.md) 第 8 节问题 5、[features.md](../product/features.md)「通话记录」、[data-flow.md](../architecture/data-flow.md#9-通话信令)、[计划](../plans/completed/call-record.md)

开发者 2026-11-23 同意实施。第 4 节的三个问题没有单独答复，按推荐方案执行：一通通话写一条，忙线不记录，点击记录回拨。实施中的调整见第 5 节。

## 1. 背景与目标

通话目前只有信令，结束后聊天里没有任何痕迹（features.md 记为未实现）。开发者要求（2026-11-22）：语音和视频通话也作为消息出现在聊天中，按结果显示通话时长、已拒绝、已取消等。

目标：

- 每通单聊通话结束后，在两人的会话里写一条通话记录消息。双方的所有设备都能收到，离线后补拉也能看到；
- 文案按查看者是主叫还是被叫区分；
- 点击记录可以用同样的类型回拨。

不在范围内：群通话（未实现）；通话进行中实时更新记录。

## 2. 约束

- **消息不能修改**：没有编辑消息的接口。「已接听」这样的中间状态不能先写一条，结束时再改成时长；如果写两条，每通通话就会在聊天里留下两条消息。
- **写入点唯一**：通话状态在 Redis 中原子结束，每通通话只有一个 access 实例走到结束路径（`notifyEnded` → `logEnded`，`app/access/internal/biz/call.go`）。这里有结束原因和接听时间 `AnsweredAt`，适合作为写入点。
- **客户端不可信**：客户端经 WebSocket 发来的消息不能带通话记录，否则可以伪造任意时长的记录。
- **内部接口不适用**：`MessageInternalService.SendMessage` 的 `session_id`、`connection_id` 必填，并且会做发言权限检查；服务端生成的记录没有会话和连接，通话在邀请时已经检查过好友关系。

## 3. 方案

### 3.1 一通通话一条消息，结束时写入

通话结束时写一条记录，内容是通话类型、结果和时长。发送者是主叫，所以主叫看到的记录在右侧，被叫看到的在左侧。

「已在其他设备接听」保持现状：被叫的其他设备在通话界面上提示（`CALL_END_REASON_ANSWERED_ELSEWHERE`），这只是那台设备上的通话结束，不是整通通话的结束，不写记录。整通通话结束后，所有设备都收到同一条带时长的记录。

### 3.2 协议

`api/message/v1/message.proto` 新增内容类型（message 不依赖 access 的 proto，所以通话类型单独定义）：

```proto
// CallContent 是通话记录，只能由 access 在通话结束时写入；客户端发送时返回 VALIDATOR。
message CallContent {
  CallRecordMedia media = 1;
  CallResult result = 2;
  // 只有 CALL_RESULT_COMPLETED 有值：从接听到结束的秒数（信令时间，不是媒体实际连通的时间）
  int32 duration_seconds = 3;
}

enum CallRecordMedia {
  CALL_RECORD_MEDIA_UNSPECIFIED = 0;
  CALL_RECORD_MEDIA_AUDIO = 1;
  CALL_RECORD_MEDIA_VIDEO = 2;
}

enum CallResult {
  CALL_RESULT_UNSPECIFIED = 0;
  CALL_RESULT_COMPLETED = 1;   // 接听后结束
  CALL_RESULT_CANCELLED = 2;   // 振铃中主叫取消
  CALL_RESULT_REJECTED = 3;    // 被叫拒绝
  CALL_RESULT_MISSED = 4;      // 振铃超时无人接听
  CALL_RESULT_FAILED = 5;      // 接听前连接断开，或被叫不在线
}

message MessageContent {
  oneof body {
    // ...
    CallContent call = 4;
  }
}
```

`api/message/v1/internal.proto` 新增专用接口，不复用 `SendMessage`：

```proto
// WriteCallRecord 在两人的单聊会话里写一条通话记录，发送者为主叫，不做发言权限检查。
// 以 (caller_id, "call:<call_id>") 作为幂等键，重复调用只存一条。
rpc WriteCallRecord(WriteCallRecordRequest) returns (WriteCallRecordResponse);

message WriteCallRecordRequest {
  int64 call_id = 1;
  int64 caller_id = 2;
  int64 callee_id = 3;
  CallContent call = 4;
}
```

只加字段和接口，`buf breaking` 兼容。旧客户端收到不认识的内容类型时，按现有逻辑显示「[暂不支持的消息类型]」（`realtime/convert.ts` 的 `toContent`）。

### 3.3 结果映射

| 结束情况（`CallEndReason` 与是否已接听） | `CallResult` |
|---|---|
| 已接听，任一方挂断或连接断开（`HANGUP`、`DISCONNECTED`） | `COMPLETED`，时长为结束时间减 `AnsweredAt` |
| 未接听，主叫挂断（`HANGUP`） | `CANCELLED` |
| `REJECTED` | `REJECTED` |
| `TIMEOUT` | `MISSED` |
| 未接听，`DISCONNECTED` 或 `PEER_OFFLINE` | `FAILED` |
| `ANSWERED_ELSEWHERE` | 不写（见 3.1） |
| 被叫忙线（邀请直接失败，没有建立通话） | 不写（待确认问题 2） |

### 3.4 显示

| 结果 | 主叫看到 | 被叫看到 |
|---|---|---|
| `COMPLETED` | 通话时长 01:23 | 通话时长 01:23 |
| `CANCELLED` | 已取消 | 对方已取消 |
| `REJECTED` | 对方已拒绝 | 已拒绝 |
| `MISSED` | 对方无应答 | 未接听 |
| `FAILED` | 连接失败 | 未接听 |

- 气泡左侧是电话或摄像头图标，后面是文案；会话摘要显示「[语音通话]」或「[视频通话]」；
- 点击记录按同样的类型回拨，走现有的呼叫流程，忙线、不是好友等情况的提示不变；
- 记录和普通消息一样计入未读。被叫错过的来电会在会话列表上显示未读；接听过的通话结束后回到聊天页时，停在底部会自动上报已读。

### 3.5 写入流程

1. `notifyEnded` 推送结束帧后，access 在后台调用 `WriteCallRecord`，不阻塞信令处理；
2. message 计算单聊会话 ID，按 `(caller_id, "call:<call_id>")` 去重后写入，之后的事务、Outbox、推送和补拉都与普通消息相同。主叫和被叫的所有连接都会收到这条消息，包括发起通话的连接；
3. message 不可用时按 1、2、4 秒重试 3 次，仍然失败就记 WARN 并放弃。通话本身不受影响，只是缺一条记录；access 进程在写入之前退出时同样会缺这条记录。access 没有数据库，不为这种情况引入持久化；
4. 客户端发送的 `OutgoingMessage` 带 `CallContent` 时返回 `VALIDATOR`：规则写在 proto 里（`OutgoingMessage.content` 的 CEL 规则 `!has(this.call)`），由 message 的校验中间件执行（见第 5 节）。

### 3.6 前端

- `ChatMessage` 新增 `call` 内容类型，在 `realtime/convert.ts` 中转换；
- 新增通话记录气泡，文案放在 `features/call/call-text.ts`，按「我是否主叫」选择；
- 会话列表的摘要显示「[语音通话]」「[视频通话]」（消息没有系统通知，只有来电有）；
- 输入框和发件箱不会产生这种消息。

### 3.7 影响范围

| 部分 | 改动 |
|---|---|
| proto | `message.proto` 新增 `CallContent` 与两个枚举，`internal.proto` 新增 `WriteCallRecord`；`make gen` 同步生成 Go 与 TS |
| message | `WriteCallRecord` 的 service 与 biz（复用发送事务，不做权限检查）；客户端发送的 `CallContent` 由 proto 中的校验规则拒绝 |
| access | 通话结束后写记录（后台、重试、指标）；message 客户端实现写记录接口 |
| 前端 | 转换、气泡、摘要、回拨 |
| 测试 | access 的结果映射与重试；message 的幂等与拒绝客户端写入；前端的转换、文案和气泡（e2e 没有补，见第 5 节） |
| 文档 | data-flow.md 第 9 节、features.md「通话记录」、模块文档 av-call 与 message、call-signaling.md 第 8 节问题 5 的结论 |

## 4. 待确认问题

1. **一通通话写几条**：推荐结束时写一条（3.1）。备选是接听时先写一条「已接听」，结束时再写一条时长，但每通通话会在聊天里留下两条消息。
2. **忙线是否记录**：推荐第一版不记录。忙线时邀请直接失败，没有通话 ID 和结束路径，要记录就得在邀请失败的分支单独写入，并另外生成幂等键。主叫当时已经看到「对方正在通话中」的提示。
3. **点击记录回拨**：推荐支持（3.4）。

开发者 2026-11-23 同意实施，三个问题没有单独答复，按推荐方案执行。

## 5. 实施中的调整

| 调整 | 原因 |
|---|---|
| 幂等键由 `call-<call_id>` 改为 `call:<call_id>` | 客户端的 `client_msg_id` 允许 `call-123` 这样的值，可能抢先占用通话记录的键，让记录写不进去；客户端的格式规则不允许冒号，带冒号的键不会冲突 |
| 拒绝客户端写入只在一处：`OutgoingMessage.content` 上的 CEL 规则 `!has(this.call)`，由 message 的校验中间件执行 | access 不解析发送帧的内容，原样转给 message 的 `SendMessage`，客户端的发送都经过这一处；规则写在 proto 里，不必在 access 和 message 各写一遍 |
| `WriteCallRecordRequest` 加 CEL 规则 `caller_id != callee_id`，`CallContent` 的类型和结果不能为未指定、时长不能为负 | 内部接口同样按 proto 校验参数 |
| access 的 message 客户端改名为 `MessageClient`，同时实现 `MessageSender` 与 `CallRecorder`；`WriteCallRecord` 也在连接级失败时重试一次 | 共用同一个连接、熔断器与重试中间件 |
| 写记录的协程在停机时：`stop` 等待进行中的那次调用结束，之后不再重试；停机之后结束的通话不写记录 | 不让停机被重试拖住，也不在 WaitGroup 等待期间新增协程 |
| 新增指标 `marschat_call_records_total{outcome}`（`written`、`failed`、`dropped`） | 能看到记录丢失的情况 |
| 没有补 e2e 用例 | 端到端测试需要在宿主机运行全部服务，当前是容器模式；改用单元测试覆盖结果映射、重试与停机，并在浏览器里打通话验证 |

验证见[计划](../plans/completed/call-record.md)。
