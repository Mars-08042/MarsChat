import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { create } from "@bufbuild/protobuf"
import { createStore } from "zustand/vanilla"
import { CallAckSchema, CallEndReason, CallEventSchema, CallMedia, IceServerSchema, SdpType } from "@/gen/access/v1/call_pb"
import { ErrorSchema } from "@/gen/access/v1/frame_pb"
import type { MessageInitShape } from "@bufbuild/protobuf"
import type { ChatUser } from "@/features/chat/types"
import { initialCallState } from "@/stores/call"
import type { ClientBody } from "../codec"
import {
  ACK_TIMEOUT_MS,
  CONNECT_TIMEOUT_MS,
  DISCONNECTED_GRACE_MS,
  ENDED_HOLD_MS,
  SIGNAL_FRAMES_PER_SECOND,
  createCallEngine,
  toRtcIceServers,
  type CallState,
} from "./engine"

// ---------- 假的媒体设备与 RTCPeerConnection ----------

class FakeTrack {
  enabled = true
  stopped = false
  constructor(readonly kind: "audio" | "video") {}
  stop() {
    this.stopped = true
  }
}

class FakeStream {
  constructor(readonly tracks: FakeTrack[]) {}
  getTracks = () => this.tracks
  getAudioTracks = () => this.tracks.filter((t) => t.kind === "audio")
  getVideoTracks = () => this.tracks.filter((t) => t.kind === "video")
}

const fakeStream = (c: MediaStreamConstraints) => new FakeStream(c.video ? [new FakeTrack("audio"), new FakeTrack("video")] : [new FakeTrack("audio")])

type Description = { type: RTCSdpType; sdp: string }

// 只实现引擎用到的部分；信令状态按 JSEP 的规则变化，addTrack 在 stable 时触发 negotiationneeded
class FakePc {
  localDescription: Description | null = null
  remoteDescription: Description | null = null
  signalingState: RTCSignalingState = "stable"
  connectionState: RTCPeerConnectionState = "new"
  onicecandidate: ((e: { candidate: Partial<RTCIceCandidate> | null }) => void) | null = null
  ontrack: ((e: { streams: FakeStream[] }) => void) | null = null
  onnegotiationneeded: (() => void) | null = null
  onconnectionstatechange: (() => void) | null = null
  added: FakeTrack[] = []
  candidates: RTCIceCandidateInit[] = []
  closed = false

  constructor(readonly config: RTCConfiguration) {}

  addTrack(track: FakeTrack) {
    this.added.push(track)
    if (this.signalingState === "stable" && this.added.length === 1) queueMicrotask(() => this.onnegotiationneeded?.())
  }

  async setLocalDescription() {
    await Promise.resolve()
    const type: RTCSdpType = this.signalingState === "have-remote-offer" ? "answer" : "offer"
    this.localDescription = { type, sdp: `${type}-sdp` }
    this.signalingState = type === "offer" ? "have-local-offer" : "stable"
  }

  async setRemoteDescription(d: Description) {
    await Promise.resolve()
    this.remoteDescription = d
    this.signalingState = d.type === "offer" ? "have-remote-offer" : "stable"
  }

  async addIceCandidate(c: RTCIceCandidateInit) {
    await Promise.resolve()
    if (!this.remoteDescription) throw new Error("InvalidStateError")
    this.candidates.push(c)
  }

  close() {
    this.closed = true
    this.signalingState = "closed"
  }

  setConnection(state: RTCPeerConnectionState) {
    this.connectionState = state
    this.onconnectionstatechange?.()
  }

  emitCandidate(candidate: string) {
    this.onicecandidate?.({ candidate: { candidate, sdpMid: "0", sdpMLineIndex: 0, usernameFragment: "u" } })
  }
}

// ---------- 测试环境 ----------

interface SentAction {
  id: bigint
  kind: string
  value: {
    callId?: bigint
    calleeId?: bigint
    media?: CallMedia
    payload?: { case: string; value: { type?: SdpType; sdp?: string; candidate?: string } }
  }
}

const linwan: ChatUser = { id: "2", nickname: "林晚" }

function setup() {
  const store = createStore<CallState>()(() => initialCallState())
  const sent: SentAction[] = []
  const pcs: FakePc[] = []
  let nextId = 1n
  const env = { connected: true, webrtc: true, relay: false }
  const getUserMedia = vi.fn(async (c: MediaStreamConstraints) => fakeStream(c) as unknown as MediaStream)
  const loadUser = vi.fn(async (id: string): Promise<ChatUser | undefined> => ({ id, nickname: "后来取到的名字" }))
  const engine = createCallEngine({
    store,
    send: (body: ClientBody) => {
      if (!env.connected) return null
      const id = nextId++
      const action = (body as { value: { action: { case: string; value: SentAction["value"] } } }).value.action
      sent.push({ id, kind: action.case, value: action.value })
      return id
    },
    getUserMedia,
    createPeerConnection: (config) => {
      const pc = new FakePc(config)
      pcs.push(pc)
      return pc as unknown as RTCPeerConnection
    },
    webrtcSupported: () => env.webrtc,
    iceTransportPolicy: () => (env.relay ? "relay" : "all"),
    user: (id) => (id === linwan.id ? linwan : undefined),
    loadUser,
  })

  const call = () => store.getState().call
  const last = (kind: string) => [...sent].reverse().find((s) => s.kind === kind)
  const signals = () => sent.filter((s) => s.kind === "signal")
  const ack = (kind: string, callId = 0n, iceServers: MessageInitShape<typeof CallAckSchema>["iceServers"] = []) =>
    engine.handleAck(last(kind)!.id, create(CallAckSchema, { callId, iceServers }))
  const error = (kind: string, reason: string) => engine.handleError(last(kind)!.id, create(ErrorSchema, { code: 409, reason }))
  const event = (init: MessageInitShape<typeof CallEventSchema>["event"]) => engine.handleEvent(create(CallEventSchema, { event: init }))
  const remoteSdp = (callId: bigint, type: SdpType) => event({ case: "signal", value: { callId, payload: { case: "sdp", value: { type, sdp: "remote-sdp" } } } })
  const remoteIce = (callId: bigint, candidate: string) =>
    event({ case: "signal", value: { callId, payload: { case: "iceCandidate", value: { candidate, sdpMid: "0", sdpMlineIndex: 0 } } } })
  const local = () => call()?.localStream as unknown as FakeStream | undefined

  return { store, engine, sent, pcs, env, getUserMedia, loadUser, call, last, signals, ack, error, event, remoteSdp, remoteIce, local }
}

const flush = () => vi.advanceTimersByTimeAsync(0)

// 主叫走到「对方已接听、offer 已发出」
async function dialAndAccept(t: ReturnType<typeof setup>, media: "audio" | "video" = "video") {
  t.engine.start(linwan, media)
  await flush()
  t.ack("invite", 9n)
  t.event({ case: "accepted", value: { callId: 9n } })
  await flush()
  return t.pcs[0]
}

// 被叫走到「已接听、收到 CallAck」
async function ringAndAnswer(t: ReturnType<typeof setup>) {
  t.event({ case: "incoming", value: { callId: 7n, callerId: 2n, media: CallMedia.AUDIO } })
  t.engine.accept()
  await flush()
  t.ack("accept", 7n)
  return t.pcs[0]
}

beforeEach(() => {
  vi.useFakeTimers()
})

afterEach(() => {
  vi.useRealTimers()
})

describe("主叫", () => {
  it("拿到设备后发出邀请；接听后发 offer，交换 answer 与候选，连通后进入通话；挂断时释放设备", async () => {
    const t = setup()
    t.engine.start(linwan, "video")
    expect(t.call()?.phase.kind).toBe("dialing")
    expect(t.getUserMedia).toHaveBeenCalledWith({ audio: true, video: true })
    await flush()
    expect(t.last("invite")?.value).toMatchObject({ calleeId: 2n, media: CallMedia.VIDEO })

    t.ack("invite", 9n)
    expect(t.call()).toMatchObject({ callId: 9n, phase: { kind: "outgoing" } })
    expect(t.engine.inProgress()).toBe(true)

    t.event({ case: "accepted", value: { callId: 9n } })
    expect(t.call()?.phase.kind).toBe("connecting")
    await flush()
    const pc = t.pcs[0]
    // 服务端没有配置 TURN：CallAck 不带 ICE 服务器，只用本机地址
    expect(pc.config).toEqual({ iceServers: [], iceTransportPolicy: "all" })
    expect(pc.added.map((tr) => tr.kind)).toEqual(["audio", "video"])
    expect(t.signals()[0].value).toMatchObject({ callId: 9n, payload: { case: "sdp", value: { type: SdpType.OFFER, sdp: "offer-sdp" } } })

    pc.emitCandidate("candidate:1")
    pc.onicecandidate?.({ candidate: null })
    expect(t.signals()).toHaveLength(2)
    expect(t.signals()[1].value.payload).toMatchObject({ case: "iceCandidate", value: { candidate: "candidate:1" } })

    t.remoteSdp(9n, SdpType.ANSWER)
    await flush()
    expect(pc.remoteDescription).toEqual({ type: "answer", sdp: "remote-sdp" })

    const remote = new FakeStream([new FakeTrack("audio")])
    pc.ontrack?.({ streams: [remote] })
    pc.setConnection("connected")
    expect(t.call()).toMatchObject({ phase: { kind: "active" }, connected: true })
    expect(t.call()?.remoteStream).toBe(remote)

    vi.advanceTimersByTime(65_000)
    const tracks = t.local()!.tracks
    t.engine.hangup()
    expect(t.last("hangup")?.value.callId).toBe(9n)
    expect(t.call()?.phase).toEqual({ kind: "ended", reason: "local-hangup", duration: 65_000 })
    expect(pc.closed).toBe(true)
    expect(tracks.every((tr) => tr.stopped)).toBe(true)
    expect(t.call()?.localStream).toBeUndefined()

    vi.advanceTimersByTime(ENDED_HOLD_MS)
    expect(t.call()).toBeNull()
  })

  it("设备不可用时不发邀请，只提示原因", async () => {
    const t = setup()
    t.getUserMedia.mockRejectedValueOnce(Object.assign(new Error("denied"), { name: "NotAllowedError" }))
    t.engine.start(linwan, "audio")
    await flush()
    expect(t.sent).toHaveLength(0)
    expect(t.call()).toBeNull()
    expect(t.store.getState().notice?.text).toBe("没有麦克风权限，请在浏览器地址栏的权限设置中允许")
  })

  it("邀请被拒绝（忙线）时释放设备，提示原因，不进入结束画面", async () => {
    const t = setup()
    t.engine.start(linwan, "audio")
    await flush()
    const tracks = t.local()!.tracks
    t.error("invite", "CALL_BUSY")
    expect(t.call()).toBeNull()
    expect(t.store.getState().notice?.text).toBe("对方正在通话中，请稍后再拨")
    expect(tracks.every((tr) => tr.stopped)).toBe(true)
  })

  it("浏览器不能使用 WebRTC 时不申请设备、不发邀请", async () => {
    const t = setup()
    t.env.webrtc = false
    t.engine.start(linwan, "video")
    await flush()
    expect(t.getUserMedia).not.toHaveBeenCalled()
    expect(t.sent).toHaveLength(0)
    expect(t.call()).toBeNull()
    expect(t.store.getState().notice?.text).toBe("当前浏览器无法通话：WebRTC 不可用，可能被浏览器扩展禁用")
  })

  it("实时连接没建立时不发邀请", async () => {
    const t = setup()
    t.env.connected = false
    t.engine.start(linwan, "audio")
    await flush()
    expect(t.call()).toBeNull()
    expect(t.store.getState().notice?.text).toBe("正在连接服务器，请稍后再拨")
  })

  it("邀请发出后、回复到达前取消：显示已取消，回复到达后补发挂断", async () => {
    const t = setup()
    t.engine.start(linwan, "audio")
    await flush()
    t.engine.hangup()
    expect(t.call()?.phase).toMatchObject({ kind: "ended", reason: "local-hangup" })
    t.ack("invite", 9n)
    expect(t.last("hangup")?.value.callId).toBe(9n)
  })

  it("申请设备期间取消：拿到设备后立即停止，不发邀请", async () => {
    const t = setup()
    t.engine.start(linwan, "video")
    t.engine.hangup()
    await flush()
    expect(t.sent).toHaveLength(0)
    expect(t.getUserMedia.mock.results[0].type).toBe("return")
    const stream = (await t.getUserMedia.mock.results[0].value) as unknown as FakeStream
    expect(stream.tracks.every((tr) => tr.stopped)).toBe(true)
  })

  it("邀请等回复超时按失败处理，迟到的回复补发挂断", async () => {
    const t = setup()
    t.engine.start(linwan, "audio")
    await flush()
    vi.advanceTimersByTime(ACK_TIMEOUT_MS)
    expect(t.call()).toBeNull()
    expect(t.store.getState().notice?.text).toBe("呼叫失败，请稍后重试")
    t.ack("invite", 9n)
    expect(t.last("hangup")?.value.callId).toBe(9n)
  })

  it("服务端结束：对方拒绝、无人接听、不在线，各自显示对应原因", async () => {
    for (const [reason, expected] of [
      [CallEndReason.REJECTED, "rejected"],
      [CallEndReason.TIMEOUT, "timeout"],
      [CallEndReason.PEER_OFFLINE, "peer-offline"],
    ] as const) {
      const t = setup()
      t.engine.start(linwan, "audio")
      await flush()
      t.ack("invite", 9n)
      t.event({ case: "ended", value: { callId: 9n, reason } })
      expect(t.call()?.phase).toEqual({ kind: "ended", reason: expected, duration: undefined })
      expect(t.engine.inProgress()).toBe(false)
    }
  })

  it("其他通话的事件不影响当前通话", async () => {
    const t = setup()
    await dialAndAccept(t)
    t.event({ case: "ended", value: { callId: 8n, reason: CallEndReason.HANGUP } })
    expect(t.call()?.phase.kind).toBe("connecting")
  })

  it("对方的候选早于 answer 到达时先缓存，设置远端描述后再加入", async () => {
    const t = setup()
    const pc = await dialAndAccept(t)
    t.remoteIce(9n, "candidate:early")
    await flush()
    expect(pc.candidates).toHaveLength(0)
    t.remoteSdp(9n, SdpType.ANSWER)
    await flush()
    expect(pc.candidates.map((c) => c.candidate)).toEqual(["candidate:early"])
    t.remoteIce(9n, "candidate:late")
    t.remoteIce(9n, "")
    await flush()
    expect(pc.candidates.map((c) => c.candidate)).toEqual(["candidate:early", "candidate:late"])
  })

  it("同时发出 offer 时，impolite 的主叫忽略对方的 offer", async () => {
    const t = setup()
    const pc = await dialAndAccept(t)
    expect(pc.signalingState).toBe("have-local-offer")
    t.remoteSdp(9n, SdpType.OFFER)
    await flush()
    expect(pc.remoteDescription).toBeNull()
    expect(t.signals().filter((s) => s.value.payload?.case === "sdp")).toHaveLength(1)
  })
})

describe("被叫", () => {
  it("来电振铃；接听后等 offer，收到后才加入本地轨道并回 answer；缓存的候选在设置远端描述后加入", async () => {
    const t = setup()
    t.event({ case: "incoming", value: { callId: 7n, callerId: 2n, media: CallMedia.AUDIO } })
    expect(t.call()).toMatchObject({ direction: "incoming", callId: 7n, peer: linwan, media: "audio", phase: { kind: "incoming" } })
    expect(t.engine.inProgress()).toBe(false)

    t.engine.accept()
    expect(t.call()?.phase.kind).toBe("connecting")
    expect(t.getUserMedia).toHaveBeenCalledWith({ audio: true, video: false })
    await flush()
    expect(t.last("accept")?.value.callId).toBe(7n)
    t.ack("accept", 7n)
    const pc = t.pcs[0]
    await flush()
    expect(pc.added).toHaveLength(0)
    expect(t.signals()).toHaveLength(0)

    t.remoteIce(7n, "candidate:early")
    t.remoteSdp(7n, SdpType.OFFER)
    await flush()
    expect(pc.candidates.map((c) => c.candidate)).toEqual(["candidate:early"])
    expect(pc.added.map((tr) => tr.kind)).toEqual(["audio"])
    expect(t.signals()[0].value).toMatchObject({ callId: 7n, payload: { case: "sdp", value: { type: SdpType.ANSWER } } })
    pc.setConnection("connected")
    expect(t.call()).toMatchObject({ phase: { kind: "active" }, connected: true })
  })

  it("不认识来电方时先显示 ID，取到资料后替换", async () => {
    const t = setup()
    t.event({ case: "incoming", value: { callId: 7n, callerId: 5n, media: CallMedia.VIDEO } })
    expect(t.call()?.peer).toEqual({ id: "5", nickname: "用户 5" })
    await flush()
    expect(t.loadUser).toHaveBeenCalledWith("5")
    expect(t.call()?.peer.nickname).toBe("后来取到的名字")
  })

  it("拒绝：发出拒绝，直接收起，不提示", () => {
    const t = setup()
    t.event({ case: "incoming", value: { callId: 7n, callerId: 2n, media: CallMedia.AUDIO } })
    t.engine.reject()
    expect(t.last("reject")?.value.callId).toBe(7n)
    expect(t.call()).toBeNull()
    expect(t.store.getState().notice).toBeNull()
  })

  it("在本端接听前结束：不弹出通话界面，只提示原因", () => {
    for (const [reason, text] of [
      [CallEndReason.ANSWERED_ELSEWHERE, "已在其他设备接听"],
      [CallEndReason.HANGUP, "对方已取消"],
      [CallEndReason.TIMEOUT, "未接来电"],
    ] as const) {
      const t = setup()
      t.event({ case: "incoming", value: { callId: 7n, callerId: 2n, media: CallMedia.AUDIO } })
      t.event({ case: "ended", value: { callId: 7n, reason } })
      expect(t.call()).toBeNull()
      expect(t.store.getState().notice?.text).toBe(text)
    }
  })

  it("服务端一直没有结束通知时，本地最多响铃 65 秒", () => {
    const t = setup()
    t.event({ case: "incoming", value: { callId: 7n, callerId: 2n, media: CallMedia.AUDIO } })
    vi.advanceTimersByTime(64_999)
    expect(t.call()?.phase.kind).toBe("incoming")
    vi.advanceTimersByTime(1)
    expect(t.call()).toBeNull()
    expect(t.store.getState().notice?.text).toBe("未接来电")
  })

  it("接听时已被其他设备接听", async () => {
    const t = setup()
    t.event({ case: "incoming", value: { callId: 7n, callerId: 2n, media: CallMedia.AUDIO } })
    t.engine.accept()
    await flush()
    const tracks = t.local()!.tracks
    t.error("accept", "CALL_ALREADY_ANSWERED")
    expect(t.call()?.phase).toMatchObject({ kind: "ended", reason: "answered-elsewhere" })
    expect(tracks.every((tr) => tr.stopped)).toBe(true)
  })

  it("接听被暂时拒绝时回到响铃，可以再接", async () => {
    const t = setup()
    t.event({ case: "incoming", value: { callId: 7n, callerId: 2n, media: CallMedia.AUDIO } })
    t.engine.accept()
    await flush()
    t.error("accept", "UPSTREAM_UNAVAILABLE")
    expect(t.call()?.phase.kind).toBe("incoming")
    expect(t.store.getState().notice?.text).toBe("接听失败，请再试一次")
    t.engine.accept()
    await flush()
    expect(t.sent.filter((s) => s.kind === "accept")).toHaveLength(2)
  })

  it("接听时麦克风不可用：继续响铃并提示", async () => {
    const t = setup()
    t.getUserMedia.mockRejectedValueOnce(Object.assign(new Error("busy"), { name: "NotReadableError" }))
    t.event({ case: "incoming", value: { callId: 7n, callerId: 2n, media: CallMedia.AUDIO } })
    t.engine.accept()
    await flush()
    expect(t.call()?.phase.kind).toBe("incoming")
    expect(t.sent).toHaveLength(0)
    expect(t.store.getState().notice?.text).toBe("麦克风正被其他程序占用")
  })

  it("浏览器不能使用 WebRTC 时接听只提示，来电继续响铃", () => {
    const t = setup()
    t.env.webrtc = false
    t.event({ case: "incoming", value: { callId: 7n, callerId: 2n, media: CallMedia.AUDIO } })
    t.engine.accept()
    expect(t.call()?.phase.kind).toBe("incoming")
    expect(t.getUserMedia).not.toHaveBeenCalled()
    expect(t.store.getState().notice?.text).toBe("当前浏览器无法通话：WebRTC 不可用，可能被浏览器扩展禁用")
  })

  it("客户端停止时不拒绝振铃中的来电，其他设备还能接", () => {
    const t = setup()
    t.event({ case: "incoming", value: { callId: 7n, callerId: 2n, media: CallMedia.AUDIO } })
    t.engine.dispose()
    expect(t.sent).toHaveLength(0)
    expect(t.call()).toBeNull()
  })
})

describe("连接失败与清理", () => {
  it("接通后 15 秒仍未连通：挂断并显示网络无法直连", async () => {
    const t = setup()
    const pc = await dialAndAccept(t)
    vi.advanceTimersByTime(CONNECT_TIMEOUT_MS)
    expect(t.last("hangup")?.value.callId).toBe(9n)
    expect(t.call()?.phase).toMatchObject({ kind: "ended", reason: "media-failed" })
    expect(pc.closed).toBe(true)
  })

  it("connectionState 变为 failed 时立即结束；disconnected 在宽限期内恢复则继续", async () => {
    const t = setup()
    const pc = await ringAndAnswer(t)
    pc.setConnection("connected")
    pc.setConnection("disconnected")
    vi.advanceTimersByTime(DISCONNECTED_GRACE_MS - 1)
    pc.setConnection("connected")
    vi.advanceTimersByTime(DISCONNECTED_GRACE_MS)
    expect(t.call()?.phase.kind).toBe("active")

    pc.setConnection("failed")
    expect(t.last("hangup")?.value.callId).toBe(7n)
    expect(t.call()?.phase).toMatchObject({ kind: "ended", reason: "media-failed" })
  })

  it("disconnected 超过宽限期按连接失败结束", async () => {
    const t = setup()
    const pc = await ringAndAnswer(t)
    pc.setConnection("connected")
    pc.setConnection("disconnected")
    vi.advanceTimersByTime(DISCONNECTED_GRACE_MS)
    expect(t.call()?.phase).toMatchObject({ kind: "ended", reason: "media-failed" })
  })

  it("信令连接断开时结束通话并释放设备", async () => {
    const t = setup()
    const pc = await dialAndAccept(t)
    const tracks = t.local()!.tracks
    t.engine.connectionLost()
    expect(t.call()?.phase).toMatchObject({ kind: "ended", reason: "disconnected" })
    expect(pc.closed).toBe(true)
    expect(tracks.every((tr) => tr.stopped)).toBe(true)
  })

  it("信令被拒绝时结束通话", async () => {
    const t = setup()
    await dialAndAccept(t)
    t.error("signal", "CALL_NOT_FOUND")
    expect(t.call()?.phase).toMatchObject({ kind: "ended", reason: "disconnected" })
  })

  it("客户端停止时挂断进行中的通话，清空状态", async () => {
    const t = setup()
    await dialAndAccept(t)
    t.engine.dispose()
    expect(t.last("hangup")?.value.callId).toBe(9n)
    expect(t.call()).toBeNull()
  })

  it("信令按每秒 8 帧限速发送，超出的排队", async () => {
    const t = setup()
    const pc = await dialAndAccept(t)
    // offer 已占用 1 帧
    for (let i = 0; i < 20; i++) pc.emitCandidate(`candidate:${i}`)
    expect(t.signals()).toHaveLength(SIGNAL_FRAMES_PER_SECOND)
    vi.advanceTimersByTime(999)
    expect(t.signals()).toHaveLength(SIGNAL_FRAMES_PER_SECOND)
    vi.advanceTimersByTime(1)
    expect(t.signals()).toHaveLength(SIGNAL_FRAMES_PER_SECOND * 2)
    vi.advanceTimersByTime(1000)
    expect(t.signals()).toHaveLength(21)
    expect(t.signals().at(-1)?.value.payload?.value.candidate).toBe("candidate:19")
  })
})

describe("静音与摄像头", () => {
  it("改轨道的 enabled；设备拿到之前的设置在拿到后套用", async () => {
    const t = setup()
    t.engine.start(linwan, "video")
    t.engine.toggleMute()
    await flush()
    const [audio, video] = t.local()!.tracks
    expect(audio.enabled).toBe(false)
    t.engine.toggleCamera()
    expect(video.enabled).toBe(false)
    expect(t.call()).toMatchObject({ muted: true, cameraOff: true })
    t.engine.toggleMute()
    expect(audio.enabled).toBe(true)
  })

  it("语音通话没有摄像头开关", async () => {
    const t = setup()
    t.engine.start(linwan, "audio")
    await flush()
    t.engine.toggleCamera()
    expect(t.call()?.cameraOff).toBe(false)
  })
})

describe("TURN 中继", () => {
  const turn = { urls: ["turn:192.0.2.10:13478?transport=udp", "turn:192.0.2.10:13478?transport=tcp"], username: "1700043200:1", credential: "c2VjcmV0" }

  it("主叫用邀请回复里的 ICE 服务器创建连接", async () => {
    const t = setup()
    t.engine.start(linwan, "audio")
    await flush()
    t.ack("invite", 9n, [turn])
    t.event({ case: "accepted", value: { callId: 9n } })
    await flush()
    expect(t.pcs[0].config).toEqual({ iceServers: [turn], iceTransportPolicy: "all" })
  })

  it("被叫用接听回复里的 ICE 服务器；打开只走中继的开关时 iceTransportPolicy 为 relay", async () => {
    const t = setup()
    t.env.relay = true
    t.event({ case: "incoming", value: { callId: 7n, callerId: 2n, media: CallMedia.AUDIO } })
    t.engine.accept()
    await flush()
    t.ack("accept", 7n, [turn])
    expect(t.pcs[0].config).toEqual({ iceServers: [turn], iceTransportPolicy: "relay" })
  })

  it("转换时丢掉没有地址的项，STUN 不带用户名和密码", () => {
    expect(
      toRtcIceServers([
        create(IceServerSchema, { urls: [] }),
        create(IceServerSchema, { urls: ["stun:192.0.2.10:13478"] }),
        create(IceServerSchema, turn),
      ]),
    ).toEqual([{ urls: ["stun:192.0.2.10:13478"] }, turn])
  })
})
