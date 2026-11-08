# 通话信令不能回显给发送方

- 涉及：`internal/service/chat/server.go`、`kafka_server.go` 的 `AudioOrVideo` 分支；`web/chat-server/src/views/chat/contact/ContactChat.vue` 的 `socket.onmessage`
- 来源：基线代码注释“通话这不能回显，发回去的话就会出现两个 start_call”，结合前端处理逻辑整理

## 现象

文本消息采用服务端回显（[ADR-0003](../architecture/adr/0003-server-side-echo.md)）。如果通话信令也照此回显，主叫方会收到自己发出的 `start_call`，界面上出现两条通话请求。

## 根因

前端处理 `type=3` 消息时只看 `av_data` 里的 `messageId` 和 `type`，不检查 `send_id` 是否为自己。主叫收到自己的 `start_call` 后会弹出“收到一条来自 xxx 的通话请求”，并把自己的状态改成“可以接听或拒绝、不能发起”，与真正的主叫状态冲突。`sdp`、`candidate` 回显同样会把自己的描述当作对端描述处理。

## 解决

后端转发通话信令时只发给接收方，回显代码被注释掉。

## 预防

- 新增任何信令类型时保持“只发给对方”。
- 前端处理 `type=3` 时可以额外忽略 `send_id` 等于自己的消息，作为第二道防线。
- 若改为后端统一回显或多端同步，需要先给信令加上方向或会话标识。
