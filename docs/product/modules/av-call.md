# 音视频通话

服务端只做信令，媒体是浏览器之间的 WebRTC 点对点连接。前端已接入（2026-11-22），客户端的状态机与协商见[前端设计](../../design/frontend.md) 3.9.5。设计见 [call-signaling.md](../../design/call-signaling.md)，帧与流程见 [data-flow.md](../../architecture/data-flow.md#9-通话信令)，Redis 键见 [data-model.md](../../architecture/data-model.md#3-redis-键)，功能状态见 [features.md](../features.md)。

| 层 | 位置 |
|---|---|
| 接口 | `api/access/v1/call.proto`（`CallRequest`、`CallAck`、`CallEvent`）；`frame.proto` 的 `ClientFrame.call`、`ServerFrame.call_ack`、`ServerFrame.call_event`；`error_reason.proto` |
| access | `app/access/internal/biz/call.go`（状态机、通知、振铃定时器）、`conn.go`（分发通话帧、心跳续期、断线结束）；`internal/data/call.go`（Redis 状态与 Lua 脚本）、`data.go`（social 权限客户端、转发器） |
| 公共 | `pkg/accessclient`（按地址直连 access，delivery 与 access 共用）、`pkg/route` |
| 前端 | `web/src/realtime/call/engine.ts`（状态机、WebRTC 协商、资源释放）、`web/src/stores/call.ts`；`web/src/features/call/`（通话层、来电提醒、文案） |

要点：

- 服务端有通话状态（Redis，所有 access 实例共享）：被叫多端同时响铃，一端接听后其他端收到“已在其他设备接听”；一个用户同时只能有一通通话，呼叫忙线的用户立即失败。
- 只能呼叫好友，任一方拉黑都不能呼叫（与单聊发消息相同）；对方不在线立即返回 `CALL_PEER_OFFLINE`。
- 振铃 60 秒无人接听由服务端结束；信令连接断开时通话结束，对方收到 `DISCONNECTED`。
- SDP 与 ICE 只在接听之后、只在主叫连接与接听连接之间转发，不回显给发送方，也不发给被叫的其他设备。
- 不落库：通话记录不写入聊天记录，通话起止只记日志。

| 操作 | WebSocket 帧 | 说明 |
|---|---|---|
| 呼叫 | `CallRequest.invite{callee_id, media}` | 回复 `CallAck{call_id}`；被叫全部连接收到 `CallEvent.incoming` |
| 接听 / 拒绝 | `CallRequest.accept` / `reject{call_id}` | 只有被叫在振铃中可以发送，任一设备拒绝都结束通话 |
| 挂断 | `CallRequest.hangup{call_id}` | 主叫在任意时刻（振铃中即取消），或接听的那个连接 |
| SDP / ICE | `CallRequest.signal{call_id, sdp \| ice_candidate}` | 接听之后，对方收到 `CallEvent.signal` |
| 结束通知 | `CallEvent.ended{call_id, reason}` | 原因：`HANGUP`、`REJECTED`、`TIMEOUT`、`ANSWERED_ELSEWHERE`、`DISCONNECTED`、`PEER_OFFLINE` |

错误原因：`CALL_NOT_FOUND`、`CALL_PEER_OFFLINE`、`CALL_BUSY`、`ALREADY_IN_CALL`、`CALL_ALREADY_ANSWERED`、`INVALID_FRAME`、`UPSTREAM_UNAVAILABLE`；呼叫的权限错误沿用 social 的 `NOT_FRIENDS`、`BLOCKED`、`USER_NOT_FOUND`。

尚未实现：群通话；STUN/TURN 服务器与临时凭据下发（coturn 推迟到前端第 9 阶段，提议见 [turn-relay.md](../../design/turn-relay.md)；在此之前没有配置任何 ICE 服务器，只有能直接互通的两端，例如同一局域网，才能建立媒体连接）；通话记录；断线重连后恢复通话。
