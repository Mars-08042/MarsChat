# 设计：通话信令（重构步骤 7）

- 状态：已采纳（2026-11-21 开发者确认：第 8 节的 7 个问题均按推荐方案） | 日期：2026-11-21 | 关联：[重构计划步骤 7](../plans/completed/backend-refactor.md#7-通话信令)、[ADR-0005](../architecture/adr/0005-distributed-architecture.md)、[网关与多实例设计](gateway-multi-instance.md)、[消息链路设计](message-pipeline.md)、[av-call.md](../product/modules/av-call.md)

## 1. 背景与目标

重构计划对步骤 7 的要求：

- 信令帧（邀请、接听、拒绝、挂断、SDP、ICE）走 WebSocket；
- access 按在线路由直接转给对端所在实例，不落库、不经 Kafka；
- 对方离线时立即返回失败；
- 多端来电时，一端接听后其他端提示“已在其他设备接听”；
- coturn 推迟到前端重构时部署（已在前端第 9 阶段实施，`CallAck` 新增 `ice_servers`，见 [turn-relay.md](turn-relay.md)）。

旧基线的做法与问题（提交 `6624cea` 之前）：

- 通话是浏览器之间的 WebRTC 点对点连接，后端只转发信令。信令作为 `type=3` 的消息，与聊天消息共用一条 WSS 通道。
- 对方不在线时信令被丢弃，主叫既没有超时，也收不到“对方不在线”的提示。
- 没有通话状态：多端同时响铃时无法互斥接听，也无法判断忙线。
- `start_call`、`receive_call`、`reject_call` 写入消息表作为通话记录；`ICE_CFG` 为空，只能在可以直接互通的网络中建立连接。
- 信令不能回显给发送方，否则发送方会把自己的 SDP 当成对方的（lesson）。

目标：

- 用 protobuf 定义通话帧，接入现有的 `ClientFrame` / `ServerFrame`。
- 通话状态放在 Redis，所有 access 实例共享。靠它实现多端互斥接听、忙线判断、振铃超时，以及断线后清理。
- access 之间用现有的 `AccessInternalService.Push` 直接转发，不新增服务和 RPC。
- 用单元测试、e2e、集群测试验证信令流程。媒体能否连通留到前端重构时用浏览器验证。

不在本步骤范围内：

- 群通话；
- STUN/TURN 服务器与临时凭据下发（随 coturn 推迟，已在前端第 9 阶段实施）；
- 通话记录写入聊天记录（第 8 节问题 5）；
- 断线重连后恢复通话（第 8 节问题 4）。

## 2. 需求与约束

- **一对一通话**：音频或视频。媒体走 WebRTC 点对点，服务端只处理信令，不接触媒体流。
- **权限**：只能呼叫好友，任何一方拉黑另一方都不能呼叫，与单聊发消息的规则相同（第 8 节问题 1）。
- **一人一通**：同一用户（包括其全部设备）同一时间最多处于一通通话中，振铃中也算。呼叫忙线的用户立即失败（问题 2）。
- **多端**：
  - 被叫的全部在线连接同时响铃，任意一台接听或拒绝后，其他设备停止响铃；
  - 主叫的其他设备不感知这通通话；
  - 信令不回显给发送它的连接。
- **SDP 与 ICE 只在接听之后转发**，并且只在主叫连接与接听连接之间转发：
  - 被叫的其他设备收不到对方的媒体参数；
  - 未接听前，被叫拿不到主叫 ICE 候选中的 IP 地址。
- **超时与清理**：
  - 振铃超过 `ring_timeout`（默认 60 秒，问题 3）无人接听，自动结束；
  - 通话一方的信令连接关闭时结束通话（问题 4）；
  - access 进程崩溃没有机会清理，残留状态靠 Redis TTL 过期。
- **不落库、不经 Kafka**：信令要求实时，排队没有意义。Redis 中的通话状态只用于协调，通话结束即删除。
- **资源**：不新增容器。access 增加对 social 的 gRPC 依赖，用于呼叫前的权限校验。

## 3. 方案

### 3.1 一次通话的完整流程

主叫用户 A 用连接 `a` 呼叫被叫用户 B，B 有连接 `b1`、`b2`，分别在不同的 access 实例上。

1. `a` 发送邀请。主叫所在的 access 依次执行：
   - 调 social `CheckSendPermission` 校验权限；
   - 用 `route.List` 查 B 的在线路由，没有路由时返回 `CALL_PEER_OFFLINE`；
   - 用 Lua 原子地创建通话，同时占用 A、B 的忙线标记，任何一方已在通话中就失败；
   - 先回复 `a`：`CallAck{call_id}`；
   - 再把 `CallIncoming` 推给 `b1`、`b2`，并启动振铃定时器。
2. `b1` 接听。`b1` 所在的 access 依次执行：
   - 用 Lua 原子地把状态从 `ringing` 改为 `active`，并记下接听连接 `b1`；
   - 向 `a` 推送 `CallAccepted`；
   - 向 `b2` 推送 `CallEnded{ANSWERED_ELSEWHERE}`；
   - 回复 `b1`：`CallAck`。
3. `a` 收到 `CallAccepted` 后创建 offer 并发送 `CallSignal`，access 转给 `b1`。`b1` 回复的 answer 和双方的 ICE 候选按同样的方式在 `a`、`b1` 之间转发。
4. 任何一方挂断时，删除通话状态和双方的忙线标记，向对方推送 `CallEnded{HANGUP}`。

### 3.2 各操作的条件与通知

| 操作 | 谁能发起 | 状态变化 | 收到通知的连接 |
|---|---|---|---|
| 邀请 | 任意连接；需要是好友，双方都不在通话中，被叫有在线路由 | 无 → `ringing` | 被叫全部连接：`CallIncoming`；发起连接：`CallAck` |
| 接听 | 被叫的任一连接，状态为 `ringing` | `ringing` → `active`，记下接听连接 | 主叫连接：`CallAccepted`；被叫其他连接：`CallEnded{ANSWERED_ELSEWHERE}` |
| 拒绝 | 被叫的任一连接，状态为 `ringing` | 结束 | 主叫连接与被叫其他连接：`CallEnded{REJECTED}` |
| 挂断 | 主叫连接（任意状态，振铃中即取消），或接听连接（`active`） | 结束 | 振铃中：被叫全部连接；通话中：对方连接。收到 `CallEnded{HANGUP}` |
| SDP / ICE | 主叫连接或接听连接，状态为 `active` | 不变 | 对方连接：`CallSignal` |
| 振铃超时 | 主叫所在 access 的定时器 | `ringing` → 结束 | 主叫连接与被叫全部连接：`CallEnded{TIMEOUT}` |
| 断线 | 主叫连接或接听连接关闭 | 结束 | 振铃中：被叫全部连接；通话中：对方连接。收到 `CallEnded{DISCONNECTED}` |

规则：

- 被叫正在响铃但没有接听的连接断开时，不影响这通通话。
- 不满足条件的操作返回错误，状态不变：
  - 通话已结束、当前连接不是通话方，或状态不对：`CALL_NOT_FOUND`；
  - 已经被另一台设备接听后再接听或拒绝：`CALL_ALREADY_ANSWERED`。
- 发给“被叫全部连接”的通知在发送时重新查路由，是尽力而为：部分连接推送失败不影响结果。邀请时如果全部失败（路由残留、连接其实都已断开），按对方离线结束通话，主叫收到 `CallEnded{PEER_OFFLINE}`。
- 转发给已记下的对方连接（`CallAccepted`、`CallSignal`）时，如果实例不可达或连接已经不在，按断线结束通话，发起请求的连接与对方都收到 `CallEnded{DISCONNECTED}`（请求此前已经得到 `CallAck`，见第 9 节第 1 项）。否则对方所在的 access 崩溃后，存活一方的心跳会一直续期这通通话（3.5 节）。

### 3.3 通话状态（Redis）

| 键 | 类型 | 内容 |
|---|---|---|
| `access:call:{call_id}` | Hash | `caller_id`、`caller_conn`、`caller_addr`、`callee_id`、`callee_conn`、`callee_addr`（接听后写入）、`media`、`state`（`ringing` / `active`）、`created_at`、`answered_at` |
| `access:call_user:{uid}` | String | 该用户当前所在通话的 `call_id`（忙线标记） |

TTL：

- 振铃中：`ring_timeout + 15s`。正常情况下由定时器提前结束；主叫所在实例崩溃时靠它兜底。
- 通话中：`heartbeat_timeout`（90 秒），双方连接每次心跳时续期。两方所在的 access 都崩溃时，最迟 90 秒后过期，忙线标记随之释放。

原子操作都用 Lua 脚本，与现有的 `pkg/idgen`、`pkg/ratelimit` 一致：

- **创建**：检查双方的忙线标记，任一方已存在就失败，否则写 Hash 和两个忙线标记（实施时去掉了“残留覆盖”，见第 9 节第 2 项）。
- **接听**：要求通话存在、`callee_id` 是当前用户、状态为 `ringing`。改为 `active`，写入接听连接，三个键的 TTL 改为通话中的 TTL。
- **结束**：按操作类型校验发起方与状态（见 3.2 节），然后删除 Hash，并删除两个忙线标记。删除标记前先比较值是否等于本通话的 `call_id`，避免误删同一用户后来的另一通通话。
- **续期**：通话存在、状态为 `active`、发起续期的是主叫连接或接听连接时，延长三个键的 TTL。

脚本要操作两个忙线标记，键名中的用户 ID 由 Go 代码先读 Hash 得到，再作为 `KEYS` 传入，脚本内再校验一次。一通通话的主叫和被叫不会变，所以先读后校验是安全的。

当前是单机 Redis。将来改用 Redis Cluster 时，三个键需要用 hash tag 放进同一个槽，这一点记在风险中。

`call_id` 是随机 63 位整数，生成方式与连接 ID 相同，创建时由脚本确认不存在。

### 3.4 access 之间的转发

- 发给“被叫全部连接”时，用 `route.List` 查路由，按实例地址分组后调用 `AccessInternalService.Push`。发给已记下的连接时，直接用 Hash 中的 `*_conn`、`*_addr`。
- 复用现有的 `Push`（`connection_ids` 加 `ServerFrame`），不新增 RPC。通话帧只是 `ServerFrame` 的一种内容。
- 目标是本实例时直接交给 Hub，不绕 gRPC。
- delivery 已经实现了按地址直连 access 并缓存连接，实例不可达时摘除并冷却（TD-31）。access 需要同样的逻辑。推荐把它移到 `pkg/accessclient`，由 delivery 与 access 共用（问题 6）。

### 3.5 超时与清理

- **振铃超时**：主叫所在的 access 在创建通话后启动进程内定时器（`time.AfterFunc`）。到期时执行“结束”脚本，条件是状态仍为 `ringing`，执行成功才发通知。接听、拒绝、取消先发生时，脚本条件不满足，定时器什么也不做。access 停机时取消全部定时器。
- **断线**：连接关闭时（`Conn.serve` 退出，包括心跳超时、被踢、停机排空），如果它是某通通话的主叫连接或接听连接，就按断线结束通话。连接只记录自己参与的 `call_id` 和对方用户 ID，由读协程维护。对方结束通话时这条记录不会被清掉，所以后续的续期和结束都要经过脚本校验。
- **续期**：连接处理心跳时，在续期在线路由之后，如果它参与着一通通话，就续期通话状态。
- **access 崩溃**：没有清理的机会。
  - 振铃中：被叫设备会一直响铃，直到客户端自己的振铃上限，客户端约定为 `ring_timeout + 5s`。Redis 中的状态按 TTL 过期。
  - 通话中：存活一方下次转发信令失败时，通话结束（3.2 节）。存活一方的 WebRTC 也会检测到媒体中断，由客户端挂断。

### 3.6 权限与错误

- 只有邀请需要校验权限：调用 social `CheckSendPermission(sender=主叫, PEER_TYPE_USER, peer=被叫)`。
  - social 的业务错误原样返回，例如 `NOT_FRIENDS`、`BLOCKED`、`USER_NOT_FOUND`，与发消息一致。
  - social 不可用时返回 `503 UPSTREAM_UNAVAILABLE`。权限校验不能像限流那样在故障时放行。
  - 该接口只读：连接级失败时重试一次，并加熔断，配置与 message 调用 social 相同。
- 后续操作只校验通话状态。通话中删除好友或拉黑，不会中断正在进行的通话。
- Redis 不可用时，所有通话操作返回 `503 UPSTREAM_UNAVAILABLE`，不降级：没有共享状态，就无法保证互斥接听和忙线判断。
- 不能呼叫自己。帧字段校验失败时返回 `INVALID_FRAME`，校验规则写在 proto 中（protovalidate，依赖已在 `go.mod`）。

### 3.7 顺序与可靠性

- 同一连接上行的帧由读协程按顺序处理，每次转发都同步等 `Push` 返回，所以同一发送方的信令按发送顺序到达对端。不同发送方之间不保证顺序。ICE 候选早于 SDP 到达时，由客户端暂存，这是 WebRTC 的常规做法。
- 邀请时先回复 `CallAck{call_id}`，再推送 `CallIncoming`。如果顺序反过来，被叫接听得足够快时，主叫可能先收到 `CallAccepted`，后收到 `CallAck`，而在此之前主叫还不知道 `call_id`。
- 信令推送不重试：重复的 SDP 会打乱 WebRTC 协商，而且失败通常意味着连接已经不在。
- 日志：
  - 邀请、接听、结束各记一行 Info，包含 `call_id`、双方用户 ID、媒体类型、结束原因、振铃时长和通话时长；
  - 不记录 SDP 和 ICE 内容，其中含有 IP 地址。

## 4. 接口、配置与数据变化

### 4.1 帧（新增 `api/access/v1/call.proto`，`frame.proto` 新增字段，不破坏兼容）

```proto
// ClientFrame.body 新增
CallRequest call = 5;
// ServerFrame.body 新增
CallAck call_ack = 10;
CallEvent call_event = 11;

message CallRequest {
  oneof action {
    CallInvite invite = 1;
    CallAccept accept = 2;
    CallReject reject = 3;
    CallHangup hangup = 4;
    CallSignal signal = 5;
  }
}

enum CallMedia { CALL_MEDIA_UNSPECIFIED = 0; CALL_MEDIA_AUDIO = 1; CALL_MEDIA_VIDEO = 2; }

message CallInvite { int64 callee_id = 1; CallMedia media = 2; }
message CallAccept { int64 call_id = 1; }
message CallReject { int64 call_id = 1; }
message CallHangup { int64 call_id = 1; }

// 客户端上行与服务端转发共用；服务端原样转发，不解析 SDP
message CallSignal {
  int64 call_id = 1;
  oneof payload {
    SessionDescription sdp = 2;     // sdp 不超过 32 KiB
    IceCandidate ice_candidate = 3; // candidate 为空串表示候选收集结束
  }
}
message SessionDescription { SdpType type = 1; string sdp = 2; }   // SdpType: OFFER、ANSWER
message IceCandidate {
  string candidate = 1;
  string sdp_mid = 2;
  optional uint32 sdp_mline_index = 3;
  string username_fragment = 4;
}

// 请求成功的回复（request_id 与请求相同）
message CallAck { int64 call_id = 1; }

// 服务端主动推送（request_id 为 0）
message CallEvent {
  oneof event {
    CallIncoming incoming = 1;  // call_id、caller_id、media
    CallAccepted accepted = 2;  // call_id
    CallEnded ended = 3;        // call_id、reason
    CallSignal signal = 4;
  }
}

enum CallEndReason {
  CALL_END_REASON_UNSPECIFIED = 0;
  CALL_END_REASON_HANGUP = 1;             // 对方挂断；振铃中为主叫取消
  CALL_END_REASON_REJECTED = 2;           // 被叫拒绝（包括被叫在另一台设备上拒绝）
  CALL_END_REASON_TIMEOUT = 3;            // 振铃超时无人接听
  CALL_END_REASON_ANSWERED_ELSEWHERE = 4; // 只发给被叫的其他设备
  CALL_END_REASON_DISCONNECTED = 5;       // 对方连接断开或不可达
  CALL_END_REASON_PEER_OFFLINE = 6;       // 邀请时被叫的连接全部失效
}
```

字段号与具体的校验规则在实施时以 proto 文件为准，`buf breaking` 会检查兼容性。

### 4.2 错误原因（`api/access/v1/error_reason.proto`）

| 原因 | HTTP 语义码 | 场景 |
|---|---|---|
| `CALL_NOT_FOUND = 20` | 404 | 通话不存在或已结束、当前连接不是通话方、状态不允许该操作 |
| `CALL_PEER_OFFLINE = 21` | 404 | 邀请时被叫没有在线连接 |
| `CALL_BUSY = 30` | 409 | 被叫正在通话或振铃中 |
| `ALREADY_IN_CALL = 31` | 409 | 自己（包括其他设备）已在通话中 |
| `CALL_ALREADY_ANSWERED = 32` | 409 | 已被另一台设备接听后，再接听或拒绝 |

`UPSTREAM_UNAVAILABLE` 的注释从“调用 message 失败”扩展为“依赖的 message、social 或 Redis 不可用”。权限错误沿用 social 的错误原因。

### 4.3 Redis 键（写入 `data-model.md`）

见 3.3 节：`access:call:{call_id}`、`access:call_user:{uid}`。

### 4.4 配置（`app/access/configs/config.yaml`，修改后需要重启）

```yaml
# 呼叫前校验权限
social:
  endpoint: ${SOCIAL_ENDPOINT:discovery:///marschat-social}
  timeout: 1s

call:
  # 振铃超时，到期无人接听自动结束
  ring_timeout: 60s
  # access 之间转发信令的单次超时
  forward_timeout: 2s
  # 对端 access 实例不可达后跳过它的时长
  forward_cooldown: 5s
```

`social` 复用现有的 `Upstream` 消息。Compose 不需要改：容器内默认经 Consul 发现 social。

### 4.5 依赖与部署

- 新增调用关系 access → social（gRPC，经 Consul），写入 `system.md` 与 `ARCHITECTURE.md`。
- social 不加入 access 的就绪检查：social 不可用只影响发起呼叫，连接和消息照常工作。
- 不新增 Go 依赖、容器和端口。

## 5. 候选方案与权衡

| 问题 | 采用 | 备选与不采用的原因 |
|---|---|---|
| 信令在哪里处理 | access 直接转发（计划既定） | 新建 call 服务：状态集中，access 只做转发，但多一跳、多一个服务，当前规模不值得。经 message 和 Kafka：计划明确不经 Kafka，信令要求实时，排队没有意义 |
| 通话状态 | Redis 共享状态加 Lua 原子操作 | 无状态纯转发（旧基线）：无法互斥接听，也无法判断忙线。只存在主叫连接的内存里：被叫的多台设备在不同实例上，接听互斥需要跨实例的原子判断 |
| SDP 何时发送 | 接听后由主叫创建 offer | 随邀请发送 offer，可以省一个往返，但 offer 要发给被叫的全部设备，ICE 候选中的 IP 在接听前就暴露给对方 |
| 转发接口 | 复用 `Push` | 新增 `ForwardSignal` RPC：没有额外的语义需要表达 |
| 振铃超时 | 服务端定时器，客户端兜底 | 只靠客户端：多端时由哪台设备判定说不清；主叫断线时，被叫没有人通知 |
| 断线后 | 立即结束 | 保留 N 秒等客户端重连后恢复：要多一种帧和一个状态（问题 4） |

## 6. 风险与验证

| 风险 | 影响 | 处理 |
|---|---|---|
| access 滚动重启或断线会结束通话 | 媒体是点对点的，本来可以继续，但信令连接断开后通话被结束 | 问题 4。重启很少发生，先保持简单；“通话恢复”记为后续扩展 |
| 主叫所在的 access 崩溃 | 被叫设备继续响铃，直到客户端的振铃上限 | 客户端约定振铃上限；Redis 状态按 TTL 过期 |
| 一方所在的 access 崩溃，另一方心跳持续续期 | 通话状态保持到存活方挂断或下一次转发失败 | 转发失败即结束；客户端在 WebRTC 连接失败时挂断 |
| ICE 候选集中发送时触发帧频率限制（每秒 30 帧） | 连接以 4008 断开 | 估计每端的候选在十几个以内，低于上限（未实测）。前端重构时实测，必要时调整策略（可热更新） |
| 振铃期间新上线的被叫设备收不到来电 | 新设备不响铃 | 不补发；接听只需要一台设备 |
| 将来改用 Redis Cluster | 多键脚本要求键在同一个槽 | 改用 hash tag，届时调整键名 |

验证：

- **单元测试**：
  - access biz 的通话状态机，用 miniredis 运行 Lua，用假的 social 和假的转发器，覆盖 3.2 节每一行的成功与失败分支；
  - 振铃超时（短超时）、断线结束、续期；
  - 忙线标记不误删、不误续期。
- **e2e**（宿主机模式，`test/e2e`）：
  - 邀请；
  - 被叫两个连接同时收到来电，一端接听后另一端收到 `ANSWERED_ELSEWHERE`；
  - SDP 与 ICE 只到达接听连接，不回显给发送方；
  - 挂断与拒绝；
  - 对方离线、忙线、自己已在通话中、非好友、呼叫自己；
  - 主叫断线后被叫收到 `DISCONNECTED`。
  振铃超时只用单元测试覆盖，e2e 不等 60 秒。
- **集群测试**（`test/cluster`）：
  - 主叫与被叫连在不同的 access 实例上，跑完整流程；
  - 杀掉接听方所在的 access 实例后，主叫下一次转发失败、通话结束，忙线标记释放；被叫重连后可以再次被呼叫。
- **检查顺序**：`buf lint` → `make gen` 后无差异 → `go vet`（含 `e2e`、`cluster` 标签）→ `go build` → `go test -race ./...` → `go mod tidy -diff`。
- **未在本步骤验证**：真实媒体能否连通（没有前端）。前端重构时用两个浏览器验证。

## 7. 实施顺序

1. **proto 与公共代码**：
   - 编写 `call.proto`，在 `frame.proto` 中加字段，加错误原因，执行 `make gen`；
   - 抽取 `pkg/accessclient`，delivery 改用它，delivery 的单元测试照常通过。
2. **access**：
   - social 客户端与配置；
   - 通话状态仓库（Lua）与通话用例；
   - Conn 接入：处理通话帧、心跳续期、断线结束；
   - 单元测试。
3. **测试**：e2e 与集群测试。
4. **文档**：
   - `data-flow.md` 新增“通话信令”一节，替换开头的“尚未重写”说明；
   - `data-model.md` 加 Redis 键；
   - `features.md` 与 `av-call.md` 加“新实现”一节；
   - `system.md`、`ARCHITECTURE.md`（第 38 行的旧描述）写入 access → social；
   - `deployment.md` 加配置项；
   - 更新重构计划。

## 8. 待确认问题

1. **呼叫权限**：沿用单聊规则（是好友，且双方都没有拉黑），调用 social `CheckSendPermission`；access 因此新增对 social 的依赖。推荐采用。
2. **一人一通与忙线**：同一用户（包括全部设备）同时只能有一通通话，振铃中也算。被叫忙线返回 `CALL_BUSY`，自己已在通话中返回 `ALREADY_IN_CALL`。推荐采用。
3. **振铃超时**：60 秒，由主叫所在的 access 判定，客户端另设振铃上限兜底。推荐采用。
4. **断线即结束**：信令连接断开时结束通话，包括 access 滚动重启时的 1001 关闭。
   - 推荐采用，“通话恢复”记为后续扩展；
   - 备选：断线后保留通话若干秒，客户端重连后发送 `CallResume` 重新绑定连接。要多一种帧、一个状态和一个定时器。
5. **通话记录**：本步骤不写入聊天记录，与计划的“不落库”一致；通话起止只记日志，`features.md` 中记为未实现。
   - 推荐本步骤不做；
   - 备选：通话结束时由 access 调 message 写一条“通话记录”消息。这要扩展消息类型，属于 message 协议变更，建议与前端一起做。
   - 后续：2026-11-23 按备选方案实现，设计见 [call-record.md](call-record.md)。
6. **抽取 `pkg/accessclient`**：把 delivery 的 access 客户端缓存（按地址直连、不可达时冷却，TD-31）移到 `pkg`，供 delivery 与 access 共用。这是 delivery 的小范围重构，行为不变。推荐采用。
7. **推迟项**：群通话，以及 ICE 服务器下发（STUN 地址、TURN 临时凭据），随 coturn 推迟到前端重构。推荐采用。

确认结果（2026-11-21）：7 个问题均按推荐方案。

## 9. 实施中的调整

实施中与上文的差异和决定，随各段完成补充。

| # | 项 | 实施 | 原因 |
|---|---|---|---|
| 1 | 回复与失败的约定 | 3.2 节原写“转发给对方失败时，发起请求的连接收到 `CALL_NOT_FOUND`”。实施改为：状态变更成功（或信令校验通过）后**先回复 `CallAck`**，再推送通知；之后转发失败按断线结束，发起请求的连接也收到 `CallEnded{DISCONNECTED}`。`Error` 只表示请求被拒绝、状态没变 | 接听时如果先通知主叫、后回复接听连接，主叫收到 `CallAccepted` 后发出的 offer 可能先于 `CallAck` 到达接听连接（与 3.7 节邀请的顺序问题相同）。先回复就必须用事件报告后续失败，客户端规则也更简单：通话结束一律看 `CallEnded` |
| 2 | 去掉“残留忙线标记可以覆盖” | 创建脚本只检查标记是否存在 | 三个键总在同一个脚本中设置 TTL、续期和删除（标记只在仍指向本通话时才续期或删除），Redis 没有配置淘汰策略（noeviction），不会出现只剩标记的情况。判断“标记指向的通话是否存在”要在脚本里拼键名，不符合 Redis 对脚本键的约定 |
| 3 | 接听时先通知被叫的其他设备 | 顺序为：回复接听连接 → 被叫其他设备 `ANSWERED_ELSEWHERE` → 主叫 `CallAccepted` | 通知主叫失败时通话按“通话中”结束，结束通知只发给接听连接；先让其他设备停止响铃，它们才不会一直响到客户端上限 |
| 4 | access 调用 social 的超时 | 1 秒（4.4 节原写 3 秒） | 与 message 调用同一接口的超时一致 |
| 5 | delivery 的直连客户端测试 | `app/delivery/internal/data/pusher_test.go` 随实现移到 `pkg/accessclient/accessclient_test.go`，用例不变 | 问题 6 的抽取 |

验证结果（2026-11-21）：

- `buf lint`、`make gen` 两次结果一致、`go vet`（含 `e2e`、`cluster` 标签）、`go build`、`go mod tidy -diff`、`go test -race ./...`（30 个包）通过；通话的 biz 单元测试 14 个（断线用例含 3 个子测试）加 `-race` 连续 5 次通过，Lua 脚本单元测试 3 个（miniredis）通过。
- 宿主机模式 `make e2e` 通过（新增通话 3 项：完整流程、错误与忙线、断线），六个服务日志无 ERROR。
- 重建镜像后 `make cluster-test` 17 项通过（新增 2 项）：主叫与接听设备在不同 access 实例上，邀请到另一实例收到来电 6.9 毫秒，offer 从发送到对端收到 0.5 毫秒；通话中杀掉接听方所在的 access，主叫在 kill 后 173 毫秒发出信令，1 毫秒内收到 `DISCONNECTED`（gRPC 连接已断开，转发立即失败），忙线标记已释放，被叫重连到存活实例后可以再次被呼叫。
- 两次测试运行时 access 调用 social 的超时还是 3 秒（第 4 项是测试之后改的，只改了配置值）。
