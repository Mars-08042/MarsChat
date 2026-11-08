# 音视频通话

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
- `start_call`、`receive_call`、`reject_call` 会写入 `message` 表，作为通话记录；`sdp`、`candidate`、`PEER_LEAVE` 不落库。
- 信令不回显给发送方（[lessons/av-signaling-no-echo.md](../../lessons/av-signaling-no-echo.md)）。
- 对方不在线时信令被丢弃，主叫端没有超时或“对方不在线”提示。
- `ICE_CFG` 为空，只能在可以直接互通的网络中建立连接；公网使用需要配置 STUN/TURN（[deployment.md](../../deployment.md#turn-与-ice-配置)）。
- 群聊页面也显示通话按钮，但后端不转发群聊通话信令，因此群聊通话不可用。

## 相关问题

TD-11（ICE 配置与群聊通话）、TD-20（`CURRENT_PEERS`/`PEER_JOIN` 只有前端处理）。
