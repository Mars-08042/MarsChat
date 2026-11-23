// 一对一通话引擎（设计 3.9.5，信令见 call-signaling.md 与 api/access/v1/call.proto）。
// 不依赖 React：状态写入 stores/call.ts，界面只读状态、调用动作；设备和 RTCPeerConnection 由调用方注入，
// 单元测试用假的实现。
//
// - 状态：dialing（申请设备、等 CallAck）→ outgoing / incoming → connecting → active → ended → 清空；
// - 协商按 WebRTC 的 perfect negotiation 写法，被叫是 polite 一方。主叫收到 CallAccepted 后加入本地轨道，
//   由 negotiationneeded 发出 offer；被叫收到 offer 后才加入轨道，避免双方同时发 offer；
// - 对端的 ICE 候选早于远端描述到达时先缓存（call-signaling 3.7）；
// - 信令帧限速：access 按固定 1 秒窗口统计每个连接的上行帧，超过 30 帧以 4008 断开。发送队列每秒最多
//   20 帧，这里每秒最多 8 帧，加上心跳也不会超限；没有 TURN 时本机网卡多，一次可能收集出几十个候选；
// - 任何结束路径都关闭 RTCPeerConnection、停止本地轨道，释放摄像头和麦克风。

import type { MessageInitShape } from "@bufbuild/protobuf"
import type { Error as ErrorFrame } from "@/gen/access/v1/frame_pb"
import {
  CallEndReason as ProtoEndReason,
  CallMedia as ProtoMedia,
  SdpType,
  type CallAck,
  type CallEvent,
  type CallIncoming,
  type CallSignal,
  type CallSignalSchema,
  type IceServer,
} from "@/gen/access/v1/call_pb"
import type { StoreApi } from "zustand/vanilla"
import {
  CLIENT_RING_LIMIT_SECONDS,
  deviceErrorText,
  endReasonText,
  inviteErrorText,
  ACCEPT_FAILED_TEXT,
  NOT_CONNECTED_TEXT,
  WEBRTC_UNSUPPORTED_TEXT,
  type CallEndReason,
  type CallMedia,
} from "@/features/call/call-text"
import type { ChatUser } from "@/features/chat/types"
import type { ClientBody } from "../codec"

export type CallPhase =
  // 申请麦克风（视频还有摄像头），拿到权限后发出邀请，等 CallAck
  | { kind: "dialing" }
  // 已收到 CallAck，对方振铃中
  | { kind: "outgoing"; since: number }
  // 收到 CallIncoming，本端振铃中
  | { kind: "incoming"; since: number }
  // 已接听，WebRTC 协商与连通中
  | { kind: "connecting" }
  | { kind: "active"; since: number }
  | { kind: "ended"; reason: CallEndReason; duration?: number }

export interface Call {
  // 本地 ID，界面用作动画的 key；服务端的 call_id 在收到 CallAck 或 CallIncoming 后才有
  id: string
  callId?: bigint
  peer: ChatUser
  media: CallMedia
  direction: "outgoing" | "incoming"
  phase: CallPhase
  // 是否接通过，决定结束文案
  connected: boolean
  muted: boolean
  cameraOff: boolean
  minimized: boolean
  localStream?: MediaStream
  remoteStream?: MediaStream
}

export interface CallState {
  call: Call | null
  // 没有进入通话界面的结果：呼叫被拒绝、设备不可用、来电在本端接听前结束。界面显示几秒后清除
  notice: { text: string; at: number } | null
}

export interface CallEngineDeps {
  store: StoreApi<CallState>
  // 已认证时发送并返回 request_id，否则返回 null
  send: (body: ClientBody) => bigint | null
  getUserMedia: (constraints: MediaStreamConstraints) => Promise<MediaStream>
  createPeerConnection: (config: RTCConfiguration) => RTCPeerConnection
  // 浏览器能否建立 WebRTC 连接。一些防 IP 泄露的扩展会把 RTCPeerConnection 置为 undefined
  webrtcSupported: () => boolean
  // STUN/TURN 服务器来自这通通话的 CallAck（邀请或接听的回复），这里只决定是否强制只走中继：
  // 排查 TURN 部署时用 "relay"，平时 "all"（docs/design/turn-relay.md 3.3）
  iceTransportPolicy?: () => RTCIceTransportPolicy
  // 来电方的资料：本地有就直接用，没有时先显示「用户 <ID>」，取到后替换
  user: (id: string) => ChatUser | undefined
  loadUser: (id: string) => Promise<ChatUser | undefined>
  now?: () => number
  log?: { warn: (message: string, detail?: Record<string, unknown>) => void }
}

export interface CallEngine {
  start(peer: ChatUser, media: CallMedia): void
  accept(): void
  reject(): void
  hangup(): void
  toggleMute(): void
  toggleCamera(): void
  setMinimized(minimized: boolean): void
  dismissNotice(): void
  // 服务端帧：不是通话请求的回复时返回 false
  handleAck(requestId: bigint, ack: CallAck): boolean
  handleError(requestId: bigint, error: ErrorFrame): boolean
  handleEvent(event: CallEvent): void
  // 正在呼叫或通话中，离开页面前要提示
  inProgress(): boolean
  // 信令连接断开：服务端会按断线结束这通通话（call-signaling 3.5），本端同样结束
  connectionLost(): void
  // 客户端停止（退出、被踢、页面卸载）：尽力挂断，释放全部资源
  dispose(): void
}

// 邀请、接听等待回复的时间；超时按失败处理，迟到的 CallAck 会补发挂断
export const ACK_TIMEOUT_MS = 10_000
// 结束画面停留时间，让用户看清结束原因
export const ENDED_HOLD_MS = 1800
// 接通后超过这个时间仍未连通，按「网络无法直连」结束
export const CONNECT_TIMEOUT_MS = 15_000
// connectionState 变为 disconnected 后等它自己恢复的时间
export const DISCONNECTED_GRACE_MS = 10_000
// 每个滑动窗口（1 秒）内最多发出的信令帧数，见文件开头
export const SIGNAL_FRAMES_PER_SECOND = 8
const SIGNAL_WINDOW_MS = 1000

type Timer = ReturnType<typeof setTimeout>
type SignalPayload = MessageInitShape<typeof CallSignalSchema>["payload"]
type CallAction = Extract<ClientBody, { case: "call" }>["value"]["action"]

// 当前通话的运行时资源，不放进 store
interface Session {
  id: string
  callId?: bigint
  polite: boolean
  ringSince: number
  local?: MediaStream
  pc?: RTCPeerConnection
  tracksAdded: boolean
  makingOffer: boolean
  ignoreOffer: boolean
  pendingIce: RTCIceCandidateInit[]
  // CallAck 带来的 STUN/TURN 服务器；服务端没有配置 TURN 时为空，只用本机地址
  iceServers: RTCIceServer[]
  // 收到的信令逐条处理，前一条的异步协商结束后再处理下一条
  chain: Promise<void>
  outbox: SignalPayload[]
  sentAt: number[]
  flushTimer?: Timer
  timers: Map<string, Timer>
  closed: boolean
}

interface Pending {
  kind: "invite" | "accept" | "reject" | "hangup" | "signal"
  session: Session
  timer?: Timer
  // 本端已放弃（取消或等回复超时）：迟到的 CallAck 说明服务端已建立通话，要补发挂断
  cancelled?: boolean
}

const toProtoMedia = (media: CallMedia) => (media === "video" ? ProtoMedia.VIDEO : ProtoMedia.AUDIO)

const END_REASONS: Record<ProtoEndReason, CallEndReason> = {
  [ProtoEndReason.UNSPECIFIED]: "disconnected",
  [ProtoEndReason.HANGUP]: "hangup",
  [ProtoEndReason.REJECTED]: "rejected",
  [ProtoEndReason.TIMEOUT]: "timeout",
  [ProtoEndReason.ANSWERED_ELSEWHERE]: "answered-elsewhere",
  [ProtoEndReason.DISCONNECTED]: "disconnected",
  [ProtoEndReason.PEER_OFFLINE]: "peer-offline",
}

const constraints = (media: CallMedia): MediaStreamConstraints => ({ audio: true, video: media === "video" })

const errorName = (e: unknown) => (typeof e === "object" && e !== null && "name" in e && typeof e.name === "string" ? e.name : "")

const stopStream = (stream: MediaStream | undefined) => stream?.getTracks().forEach((t) => t.stop())

export function createCallEngine(deps: CallEngineDeps): CallEngine {
  const { store } = deps
  const now = deps.now ?? Date.now
  const warn = (message: string, detail?: Record<string, unknown>) => deps.log?.warn(message, detail)

  let session: Session | null = null
  let holdTimer: Timer | undefined
  const pending = new Map<bigint, Pending>()

  const current = () => store.getState().call
  const patchCall = (s: Session, patch: Partial<Call>) => {
    const c = store.getState().call
    if (c && c.id === s.id) store.setState({ call: { ...c, ...patch } })
  }
  const notice = (text: string) => store.setState({ notice: { text, at: now() } })

  const clearHold = () => {
    clearTimeout(holdTimer)
    holdTimer = undefined
  }

  const setTimer = (s: Session, name: string, ms: number, fn: () => void) => {
    clearTimeout(s.timers.get(name))
    s.timers.set(
      name,
      setTimeout(() => {
        s.timers.delete(name)
        if (!s.closed) fn()
      }, ms),
    )
  }
  const clearTimer = (s: Session, name: string) => {
    clearTimeout(s.timers.get(name))
    s.timers.delete(name)
  }

  const newSession = (polite: boolean): Session => ({
    id: crypto.randomUUID(),
    polite,
    ringSince: now(),
    tracksAdded: false,
    makingOffer: false,
    ignoreOffer: false,
    pendingIce: [],
    iceServers: [],
    chain: Promise.resolve(),
    outbox: [],
    sentAt: [],
    timers: new Map(),
    closed: false,
  })

  // 发出一个通话请求并记下来，回复（CallAck 或 Error）按 request_id 对应
  const request = (s: Session, kind: Pending["kind"], action: CallAction): boolean => {
    const requestId = deps.send({ case: "call", value: { action } })
    if (requestId === null) return false
    const p: Pending = { kind, session: s }
    if (kind === "invite" || kind === "accept") {
      p.timer = setTimeout(() => {
        p.timer = undefined
        p.cancelled = true
        if (session !== s) return
        if (kind === "invite") abort(s, inviteErrorText(""))
        else end(s, "disconnected")
      }, ACK_TIMEOUT_MS)
    }
    pending.set(requestId, p)
    return true
  }

  // 释放运行时资源：关闭连接、停止本地轨道、清掉定时器和待发的信令
  const release = (s: Session) => {
    s.closed = true
    s.timers.forEach((t) => clearTimeout(t))
    s.timers.clear()
    clearTimeout(s.flushTimer)
    s.outbox = []
    s.pendingIce = []
    s.pc?.close()
    stopStream(s.local)
  }

  // 通话没有开始就失败（设备不可用、邀请被拒绝）：不进入结束画面，只提示原因
  const abort = (s: Session, text: string) => {
    if (session !== s) return
    release(s)
    session = null
    store.setState({ call: null, notice: { text, at: now() } })
  }

  const end = (s: Session, reason: CallEndReason) => {
    if (session !== s) return
    release(s)
    session = null
    const c = current()
    if (!c || c.id !== s.id) return
    // 来电在本端接听之前结束：不弹出通话界面，只提示一句；自己拒绝的不提示
    if (c.direction === "incoming" && c.phase.kind === "incoming") {
      store.setState({
        call: null,
        notice: reason === "rejected" ? null : { text: endReasonText(reason, { direction: "incoming", connected: false }), at: now() },
      })
      return
    }
    const duration = c.phase.kind === "active" ? now() - c.phase.since : undefined
    // 结束时展开悬浮窗，让用户看到结束原因
    store.setState({ call: { ...c, phase: { kind: "ended", reason, duration }, minimized: false, localStream: undefined, remoteStream: undefined } })
    clearHold()
    holdTimer = setTimeout(() => {
      holdTimer = undefined
      if (current()?.id === c.id) store.setState({ call: null })
    }, ENDED_HOLD_MS)
  }

  // 本端主动结束接通之后的通话（挂断、媒体连接失败）
  const hangupAndEnd = (s: Session, reason: CallEndReason) => {
    if (s.callId !== undefined) request(s, "hangup", { case: "hangup", value: { callId: s.callId } })
    end(s, reason)
  }

  // 静音和关闭摄像头直接改轨道的 enabled，不重新协商；设备还没拿到时，拿到后再套用
  const applyTrackState = (s: Session) => {
    const c = current()
    if (!c || c.id !== s.id || !s.local) return
    for (const t of s.local.getAudioTracks()) t.enabled = !c.muted
    for (const t of s.local.getVideoTracks()) t.enabled = !c.cameraOff
  }

  // ---------- 信令发送 ----------

  const flushSignals = (s: Session) => {
    if (s.closed || s.flushTimer !== undefined || s.callId === undefined) return
    const t = now()
    s.sentAt = s.sentAt.filter((at) => t - at < SIGNAL_WINDOW_MS)
    while (s.outbox.length > 0) {
      if (s.sentAt.length >= SIGNAL_FRAMES_PER_SECOND) {
        s.flushTimer = setTimeout(() => {
          s.flushTimer = undefined
          flushSignals(s)
        }, s.sentAt[0] + SIGNAL_WINDOW_MS - t)
        return
      }
      const payload = s.outbox[0]
      // 连接已断开：随后的 connectionLost 会结束通话
      if (!request(s, "signal", { case: "signal", value: { callId: s.callId, payload } })) return
      s.outbox.shift()
      s.sentAt.push(t)
    }
  }

  const sendSignal = (s: Session, payload: SignalPayload) => {
    s.outbox.push(payload)
    flushSignals(s)
  }

  const sendDescription = (s: Session, d: RTCSessionDescription | null) => {
    if (!d || (d.type !== "offer" && d.type !== "answer")) return
    sendSignal(s, { case: "sdp", value: { type: d.type === "offer" ? SdpType.OFFER : SdpType.ANSWER, sdp: d.sdp } })
  }

  // ---------- WebRTC ----------

  const addLocalTracks = (s: Session) => {
    if (s.tracksAdded || !s.local || !s.pc) return
    s.tracksAdded = true
    for (const track of s.local.getTracks()) s.pc.addTrack(track, s.local)
  }

  const onConnectionState = (s: Session, state: RTCPeerConnectionState) => {
    switch (state) {
      case "connected": {
        clearTimer(s, "connect")
        clearTimer(s, "grace")
        const c = current()
        if (c?.id === s.id && c.phase.kind === "connecting") patchCall(s, { phase: { kind: "active", since: now() }, connected: true })
        return
      }
      case "disconnected":
        // 网络抖动时可能自己恢复；一直没恢复就按连接失败结束
        if (!s.timers.has("grace")) setTimer(s, "grace", DISCONNECTED_GRACE_MS, () => hangupAndEnd(s, "media-failed"))
        return
      case "failed":
        hangupAndEnd(s, "media-failed")
        return
    }
  }

  // 创建失败（浏览器在通话中途禁用了 WebRTC 等）时结束通话，返回 undefined
  const ensurePc = (s: Session): RTCPeerConnection | undefined => {
    if (s.pc) return s.pc
    let pc: RTCPeerConnection
    try {
      pc = deps.createPeerConnection({ iceServers: s.iceServers, iceTransportPolicy: deps.iceTransportPolicy?.() ?? "all" })
    } catch (e) {
      warn("create peer connection failed", { error: String(e) })
      hangupAndEnd(s, "media-failed")
      return undefined
    }
    s.pc = pc
    pc.onicecandidate = (e) => {
      // 收集结束（candidate 为 null 或空串）不发送：对端不需要它也能连通，省一帧
      const c = e.candidate
      if (!c || !c.candidate) return
      sendSignal(s, {
        case: "iceCandidate",
        value: { candidate: c.candidate, sdpMid: c.sdpMid ?? "", sdpMlineIndex: c.sdpMLineIndex ?? undefined, usernameFragment: c.usernameFragment ?? "" },
      })
    }
    pc.ontrack = (e) => {
      const stream = e.streams[0]
      if (stream && current()?.remoteStream !== stream) patchCall(s, { remoteStream: stream })
    }
    pc.onnegotiationneeded = async () => {
      try {
        s.makingOffer = true
        await pc.setLocalDescription()
        if (!s.closed) sendDescription(s, pc.localDescription)
      } catch (e) {
        if (!s.closed) warn("negotiation failed", { error: String(e) })
      } finally {
        s.makingOffer = false
      }
    }
    pc.onconnectionstatechange = () => {
      if (!s.closed) onConnectionState(s, pc.connectionState)
    }
    // 主叫立即加入轨道，由 negotiationneeded 发出 offer；被叫等收到 offer 再加
    if (!s.polite) addLocalTracks(s)
    return pc
  }

  const handleSignal = async (s: Session, signal: CallSignal) => {
    const pc = ensurePc(s)
    if (!pc) return
    const p = signal.payload
    if (p.case === "sdp") {
      const description: RTCSessionDescriptionInit = { type: p.value.type === SdpType.OFFER ? "offer" : "answer", sdp: p.value.sdp }
      // 双方同时发 offer：impolite 的主叫忽略对方的 offer，polite 的被叫回滚自己的（setRemoteDescription 隐式回滚）
      const collision = description.type === "offer" && (s.makingOffer || pc.signalingState !== "stable")
      s.ignoreOffer = !s.polite && collision
      if (s.ignoreOffer) return
      await pc.setRemoteDescription(description)
      if (s.closed) return
      const cached = s.pendingIce
      s.pendingIce = []
      for (const c of cached) await addIce(s, pc, c)
      if (description.type === "offer") {
        addLocalTracks(s)
        await pc.setLocalDescription()
        if (!s.closed) sendDescription(s, pc.localDescription)
      }
      return
    }
    if (p.case === "iceCandidate") {
      // 空串是收集结束的标记，不需要处理
      if (!p.value.candidate) return
      const init: RTCIceCandidateInit = {
        candidate: p.value.candidate,
        sdpMid: p.value.sdpMid || null,
        sdpMLineIndex: p.value.sdpMlineIndex ?? null,
        usernameFragment: p.value.usernameFragment || null,
      }
      // 早于远端描述到达的候选先缓存，设置远端描述后再加入
      if (!pc.remoteDescription) s.pendingIce.push(init)
      else await addIce(s, pc, init)
    }
  }

  const addIce = async (s: Session, pc: RTCPeerConnection, init: RTCIceCandidateInit) => {
    try {
      await pc.addIceCandidate(init)
    } catch (e) {
      // 忽略了对方的 offer 时，属于那次协商的候选加不进去，是预期的
      if (!s.ignoreOffer && !s.closed) warn("add ice candidate failed", { error: String(e) })
    }
  }

  // ---------- 服务端事件 ----------

  const sessionFor = (callId: bigint) => (session && session.callId === callId ? session : null)

  const onIncoming = (incoming: CallIncoming) => {
    const c = current()
    // 服务端保证一人一通，本端还有通话说明状态不一致，以本端为准
    if (c && c.phase.kind !== "ended") {
      warn("incoming call while busy", { callId: incoming.callId.toString() })
      return
    }
    clearHold()
    const s = newSession(true)
    s.callId = incoming.callId
    session = s
    const peerId = incoming.callerId.toString()
    const known = deps.user(peerId)
    store.setState({
      call: {
        id: s.id,
        callId: incoming.callId,
        peer: known ?? { id: peerId, nickname: `用户 ${peerId}` },
        media: incoming.media === ProtoMedia.VIDEO ? "video" : "audio",
        direction: "incoming",
        phase: { kind: "incoming", since: s.ringSince },
        connected: false,
        muted: false,
        cameraOff: false,
        minimized: false,
      },
      notice: null,
    })
    if (!known) void deps.loadUser(peerId).then((u) => u && patchCall(s, { peer: u }))
    // 主叫所在的 access 崩溃时没有人发 CallEnded，本端最多响铃 ring_timeout + 5 秒
    setTimer(s, "ring", CLIENT_RING_LIMIT_SECONDS * 1000, () => {
      if (current()?.phase.kind === "incoming") end(s, "timeout")
    })
  }

  const reject = () => {
    const s = session
    if (!s || current()?.phase.kind !== "incoming" || s.callId === undefined) return
    request(s, "reject", { case: "reject", value: { callId: s.callId } })
    end(s, "rejected")
  }

  return {
    start(peer, media) {
      const existing = current()
      if (existing && existing.phase.kind !== "ended") return
      if (!deps.webrtcSupported()) {
        notice(WEBRTC_UNSUPPORTED_TEXT)
        return
      }
      clearHold()
      const s = newSession(false)
      session = s
      store.setState({
        call: { id: s.id, peer, media, direction: "outgoing", phase: { kind: "dialing" }, connected: false, muted: false, cameraOff: false, minimized: false },
        notice: null,
      })
      // 先拿到设备权限再发邀请，避免对方接听后才发现本端没有设备
      deps.getUserMedia(constraints(media)).then(
        (stream) => {
          if (session !== s) {
            stopStream(stream)
            return
          }
          s.local = stream
          applyTrackState(s)
          patchCall(s, { localStream: stream })
          if (!request(s, "invite", { case: "invite", value: { calleeId: BigInt(peer.id), media: toProtoMedia(media) } })) abort(s, NOT_CONNECTED_TEXT)
        },
        (e: unknown) => abort(s, deviceErrorText(errorName(e), media)),
      )
    },

    accept() {
      const s = session
      const c = current()
      if (!s || !c || c.id !== s.id || c.phase.kind !== "incoming" || s.callId === undefined) return
      // 不能接听时来电继续响铃，用户可以拒绝；不自动拒绝，用户的其他设备也许能接
      if (!deps.webrtcSupported()) {
        notice(WEBRTC_UNSUPPORTED_TEXT)
        return
      }
      const callId = s.callId
      patchCall(s, { phase: { kind: "connecting" } })
      deps.getUserMedia(constraints(c.media)).then(
        (stream) => {
          if (session !== s) {
            stopStream(stream)
            return
          }
          s.local = stream
          applyTrackState(s)
          patchCall(s, { localStream: stream })
          if (!request(s, "accept", { case: "accept", value: { callId } })) end(s, "disconnected")
        },
        (e: unknown) => {
          if (session !== s) return
          // 没有接听成功，来电继续响铃，用户可以拒绝或改了权限后再接
          patchCall(s, { phase: { kind: "incoming", since: s.ringSince } })
          notice(deviceErrorText(errorName(e), c.media))
        },
      )
    },

    reject,

    hangup() {
      const s = session
      const c = current()
      if (!s || !c || c.id !== s.id) return
      switch (c.phase.kind) {
        case "ended":
          return
        case "incoming":
          reject()
          return
        case "dialing":
          // 邀请已经发出但还没有回复：等 CallAck 到了再补发挂断
          for (const p of pending.values()) if (p.session === s && p.kind === "invite") p.cancelled = true
          end(s, "local-hangup")
          return
        default:
          hangupAndEnd(s, "local-hangup")
      }
    },

    toggleMute() {
      const c = current()
      if (!c || !session || c.phase.kind === "ended") return
      patchCall(session, { muted: !c.muted })
      applyTrackState(session)
    },

    toggleCamera() {
      const c = current()
      if (!c || !session || c.media !== "video" || c.phase.kind === "ended") return
      patchCall(session, { cameraOff: !c.cameraOff })
      applyTrackState(session)
    },

    setMinimized(minimized) {
      const c = current()
      if (c) store.setState({ call: { ...c, minimized } })
    },

    dismissNotice: () => store.setState({ notice: null }),

    handleAck(requestId, ack) {
      const p = pending.get(requestId)
      if (!p) return false
      pending.delete(requestId)
      clearTimeout(p.timer)
      const s = p.session
      if (p.cancelled) {
        const callId = p.kind === "invite" ? ack.callId : s.callId
        if (callId) deps.send({ case: "call", value: { action: { case: "hangup", value: { callId } } } })
        return true
      }
      if (session !== s) return true
      // 只有邀请和接听的回复带 ICE 服务器；主叫在 CallAccepted 之后、被叫在下面接听成功时才创建连接，都已拿到
      if (p.kind === "invite" || p.kind === "accept") s.iceServers = toRtcIceServers(ack.iceServers)
      if (p.kind === "invite") {
        s.callId = ack.callId
        patchCall(s, { callId: ack.callId, phase: { kind: "outgoing", since: now() } })
      } else if (p.kind === "accept") {
        // 接听成功，等主叫的 offer
        clearTimer(s, "ring")
        ensurePc(s)
        setTimer(s, "connect", CONNECT_TIMEOUT_MS, () => hangupAndEnd(s, "media-failed"))
      }
      return true
    },

    handleError(requestId, error) {
      const p = pending.get(requestId)
      if (!p) return false
      pending.delete(requestId)
      clearTimeout(p.timer)
      const s = p.session
      if (p.cancelled || session !== s) return true
      switch (p.kind) {
        case "invite":
          abort(s, inviteErrorText(error.reason))
          return true
        case "accept":
          if (error.reason === "CALL_ALREADY_ANSWERED") end(s, "answered-elsewhere")
          else if (error.reason === "CALL_NOT_FOUND") end(s, "hangup")
          else {
            // Error 表示状态没有变化：来电仍在响铃，可以再点接听
            stopStream(s.local)
            s.local = undefined
            patchCall(s, { phase: { kind: "incoming", since: s.ringSince }, localStream: undefined })
            notice(ACCEPT_FAILED_TEXT)
          }
          return true
        case "signal":
          // 信令被拒绝（通话已在服务端结束等），协商无法继续
          end(s, "disconnected")
          return true
        default:
          return true
      }
    },

    handleEvent(event) {
      const e = event.event
      switch (e.case) {
        case "incoming":
          onIncoming(e.value)
          return
        case "accepted": {
          const s = sessionFor(e.value.callId)
          if (!s || current()?.phase.kind !== "outgoing") return
          patchCall(s, { phase: { kind: "connecting" } })
          ensurePc(s)
          setTimer(s, "connect", CONNECT_TIMEOUT_MS, () => hangupAndEnd(s, "media-failed"))
          return
        }
        case "ended": {
          const s = sessionFor(e.value.callId)
          if (s) end(s, END_REASONS[e.value.reason] ?? "disconnected")
          return
        }
        case "signal": {
          const s = sessionFor(e.value.callId)
          if (!s) return
          const signal = e.value
          s.chain = s.chain
            .then(() => handleSignal(s, signal))
            .catch((err: unknown) => {
              if (!s.closed) warn("handle signal failed", { error: String(err) })
            })
          return
        }
      }
    },

    inProgress() {
      const kind = current()?.phase.kind
      return kind === "dialing" || kind === "outgoing" || kind === "connecting" || kind === "active"
    },

    connectionLost() {
      // 断开前发出的请求不会再有回复
      pending.forEach((p) => clearTimeout(p.timer))
      pending.clear()
      if (session) end(session, "disconnected")
    },

    dispose() {
      const s = session
      const kind = current()?.phase.kind
      // 振铃中的来电不拒绝：用户可能还会在其他设备上接听
      if (s && s.callId !== undefined && kind !== "incoming") {
        deps.send({ case: "call", value: { action: { case: "hangup", value: { callId: s.callId } } } })
      }
      if (s) release(s)
      session = null
      clearHold()
      pending.forEach((p) => clearTimeout(p.timer))
      pending.clear()
      store.setState({ call: null, notice: null })
    },
  }
}

// CallAck 里的 IceServer 转成 RTCIceServer；STUN 没有用户名和密码，不带这两个字段
export function toRtcIceServers(servers: IceServer[]): RTCIceServer[] {
  return servers
    .filter((s) => s.urls.length > 0)
    .map((s) => (s.username ? { urls: [...s.urls], username: s.username, credential: s.credential } : { urls: [...s.urls] }))
}
