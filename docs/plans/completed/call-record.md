# 计划：通话记录写入聊天

- 状态：已完成 | 开始：2026-11-23 | 完成：2026-11-23 | 分支：`dev` | 关联设计：[call-record.md](../../design/call-record.md)（已采纳）

## 目标

每通单聊通话结束后，在两人的会话里写一条通话记录消息，按查看者是主叫还是被叫显示通话时长、已取消、已拒绝等，点击可以回拨。

## 结果

2026-11-23 完成。access 在通话结束时经 message 新增的内部接口 `WriteCallRecord` 写入记录，发送者为主叫，按通话 ID 幂等；客户端发送的通话记录被 proto 校验规则拒绝；前端显示通话记录气泡、会话摘要，点击回拨。实施中的调整见设计第 5 节。

遗留：忙线不记录（设计第 4 节问题 2）；message 不可用超过重试时间、或 access 在写入前退出时缺这条记录（TD-47）；没有补 e2e 用例（设计第 5 节）。

## 步骤

- [x] proto：`message.proto` 新增 `CallContent`、`CallRecordMedia`、`CallResult`，`OutgoingMessage` 拒绝通话记录；`internal.proto` 新增 `WriteCallRecord`；`make gen`。
- [x] message：`WriteCallRecord`（复用发送事务，不限流、不做权限检查，按 `call:<call_id>` 幂等）；单元测试。
- [x] access：通话结束后在后台写记录（结果映射、重试、停机时等待）；message 客户端实现写记录接口；单元测试。
- [x] 前端：内容转换、通话记录气泡与文案、会话摘要、点击回拨；单元测试。
- [x] 文档：data-flow.md 9.6、features.md、模块文档 av-call 与 chat-message、observability.md、call-signaling.md 第 8 节问题 5、frontend.md、overview.md。
- [x] 验证（见下）。

## 进度记录

2026-11-23 完成全部步骤。验证：

- `buf lint`、`buf breaking`（对 `main`，只加字段和接口）、`make gen` 后无差异、`go mod tidy -diff`、`go vet ./...`、`go build ./...`、`go test -race ./...`（33 个包）通过。新增用例：
  - message：通话记录接在已有会话后、发送者为主叫、跳过限流和权限检查、推送不排除连接、主叫已读位置推进；第一条消息就是记录时建立会话；重试只存一条，客户端的 `call-503` 不占用记录的键；`SendMessage` 带通话记录被校验拒绝；`WriteCallRecordRequest` 的参数校验；
  - access：结束原因到结果的映射（含时长四舍五入）；完整通话、取消、拒绝各写一条，`ANSWERED_ELSEWHERE` 不写；重试到成功、4xx 不重试；停机时不等待下一次重试；
- `make web-check` 通过：单元测试 452 项（32 个文件，新增通话记录文案、转换与摘要、气泡组件的文案、回拨和通话中禁用）、构建；
- 只重建了 access、message 和前端镜像与容器（`make SERVICES="access message" images`、`make web-image`），之后经网关用两个测试账号走 WebSocket 打真实的信令通话（程序在被忽略的 `tmp/callcheck`，结束时退出登录）：振铃中取消、接听后约 3 秒挂断、拒绝，主叫和被叫都收到同一条记录（seq 341～343，结果分别为 `CANCELLED`、`COMPLETED` 3 秒、`REJECTED`）；客户端发送带通话记录的消息得到 `400 VALIDATOR`；
- 浏览器：阿星一侧显示「已取消」「通话时长 00:03」「对方已拒绝」，图标在靠头像的一侧，会话摘要为「你: [视频通话]」，回拨按钮可用（没有点击，避免真的呼叫），控制台没有错误；林晚一侧由开发者查看，显示正确；
- 未验证：点击记录回拨（与顶栏的呼叫按钮走同一个 `callActions.start`，组件测试覆盖了调用参数）；message 不可用时浏览器里的表现（只有单元测试）。

## 遇到的问题

- 2026-11-23：后端检查（含 `make gen`）与 `make web-image` 同时运行。`make gen` 会先删除再重新生成 `web/src/gen`，前端构建可能读到不完整的生成代码。这次构建成功，生成代码是确定性的，产物不受影响；以后先跑完 `make gen` 再构建前端。
