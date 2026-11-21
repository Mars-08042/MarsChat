# 音视频通话

> **重构说明（2026-11-21）**：“新实现”小节描述步骤 7 的信令代码；其后各节描述已移除的旧基线（截至提交 `6624cea`），其中的代码位置已不存在，只作功能参考。

## 新实现：信令（重构步骤 7）

服务端只做信令，媒体仍是浏览器之间的 WebRTC 点对点连接，前端尚未重写。设计见 [call-signaling.md](../../design/call-signaling.md)，帧与流程见 [data-flow.md](../../architecture/data-flow.md#9-通话信令)，Redis 键见 [data-model.md](../../architecture/data-model.md#3-redis-键)，功能状态见 [features.md](../features.md#新后端)。

| 层 | 位置 |
|---|---|
| 接口 | `api/access/v1/call.proto`（`CallRequest`、`CallAck`、`CallEvent`）；`frame.proto` 的 `ClientFrame.call`、`ServerFrame.call_ack`、`ServerFrame.call_event`；`error_reason.proto` |
| access | `app/access/internal/biz/call.go`（状态机、通知、振铃定时器）、`conn.go`（分发通话帧、心跳续期、断线结束）；`internal/data/call.go`（Redis 状态与 Lua 脚本）、`data.go`（social 权限客户端、转发器） |
| 公共 | `pkg/accessclient`（按地址直连 access，delivery 与 access 共用）、`pkg/route` |

与旧基线的主要差别：

- 服务端有通话状态（Redis，所有 access 实例共享）：被叫多端同时响铃，一端接听后其他端收到“已在其他设备接听”；一个用户同时只能有一通通话，呼叫忙线的用户立即失败。
- 只能呼叫好友，任一方拉黑都不能呼叫（与单聊发消息相同）；对方不在线立即返回 `CALL_PEER_OFFLINE`，旧基线会静默丢弃。
- 振铃 60 秒无人接听由服务端结束；信令连接断开时通话结束，对方收到 `DISCONNECTED`。
- SDP 与 ICE 只在接听之后、只在主叫连接与接听连接之间转发，不回显给发送方，也不发给被叫的其他设备。
- 不落库：通话记录不写入聊天记录（旧基线写入 `message` 表），通话起止只记日志。

| 操作 | WebSocket 帧 | 说明 |
|---|---|---|
| 呼叫 | `CallRequest.invite{callee_id, media}` | 回复 `CallAck{call_id}`；被叫全部连接收到 `CallEvent.incoming` |
| 接听 / 拒绝 | `CallRequest.accept` / `reject{call_id}` | 只有被叫在振铃中可以发送，任一设备拒绝都结束通话 |
| 挂断 | `CallRequest.hangup{call_id}` | 主叫在任意时刻（振铃中即取消），或接听的那个连接 |
| SDP / ICE | `CallRequest.signal{call_id, sdp \| ice_candidate}` | 接听之后，对方收到 `CallEvent.signal` |
| 结束通知 | `CallEvent.ended{call_id, reason}` | 原因：`HANGUP`、`REJECTED`、`TIMEOUT`、`ANSWERED_ELSEWHERE`、`DISCONNECTED`、`PEER_OFFLINE` |

错误原因：`CALL_NOT_FOUND`、`CALL_PEER_OFFLINE`、`CALL_BUSY`、`ALREADY_IN_CALL`、`CALL_ALREADY_ANSWERED`、`INVALID_FRAME`、`UPSTREAM_UNAVAILABLE`；呼叫的权限错误沿用 social 的 `NOT_FRIENDS`、`BLOCKED`、`USER_NOT_FOUND`。

尚未实现：群通话；STUN/TURN 服务器与临时凭据下发（随前端重构部署 coturn，旧基线的 TD-11 在此之前仍然存在）；通话记录；断线重连后恢复通话。

## 职责

单聊双方的一对一音视频通话：发起、接听、拒绝、挂断。

## 实现方式

- 浏览器原生 WebRTC（`RTCPeerConnection` + `getUserMedia`），媒体流在两个浏览器之间点对点传输。
- 后端只充当信令通道：信令是 `type=3` 的 WebSocket 聊天消息，由转发服务原样推给对方。
- 后端没有通话状态，也没有专门的接口。
- 信令时序与 `av_data` 格式见 [data-flow.md](../../architecture/data-flow.md#6-音视频通话信令)。

## 代码位置

| 层 | 位置 |
|---|---|
| 前端 | `views/chat/contact/ContactChat.vue`：`startCall`、`rejectCall`、`sendEndCall`、`createOffer`、`createAnswer`、`handleCandidate` 等，以及通话弹窗 |
| 后端 | `internal/service/chat/server.go`、`kafka_server.go` 的 `AudioOrVideo` 分支 |

## 行为与限制

- 前端防重入：已在通话中不能再次发起；对方已发起时只能先接听或拒绝。
- 发起和接听前检查安全上下文及 WebRTC、媒体接口能力。局域网 IP 的 HTTP 页面明确提示使用受信任的 HTTPS；接口缺失、权限拒绝或设备不可用时提示原因，不继续绑定空媒体流或发送通话信令。失败时释放已获取的媒体轨道及连接；等待媒体授权期间阻止重复启动。
- `start_call`、`receive_call`、`reject_call` 会写入 `message` 表，作为通话记录；`sdp`、`candidate`、`PEER_LEAVE` 不落库。
- 信令不回显给发送方（[lessons/av-signaling-no-echo.md](../../lessons/av-signaling-no-echo.md)）。
- 对方不在线时信令被丢弃，主叫端没有超时或“对方不在线”提示。
- `ICE_CFG` 为空，只能在可以直接互通的网络中建立连接；公网使用需要配置 STUN/TURN（旧部署文档的 TURN 与 ICE 配置一节，见提交 `6624cea` 中的 `docs/deployment.md`）。
- 群聊页面也显示通话按钮，但后端不转发群聊通话信令，因此群聊通话不可用。

## 相关问题

TD-11（ICE 配置与群聊通话）、TD-20（`CURRENT_PEERS`/`PEER_JOIN` 只有前端处理）。

## 回归验证

在 `web/chat-server` 执行 `node --test tests/av-call.test.cjs`。测试运行组件实际通话函数，模拟局域网 HTTP、接口缺失、设备失败、空媒体流、正常发起与接听、重复点击和信令发送失败。2026-11-10：7 项测试通过，未使用真实摄像头或验证手机端到端通话。
