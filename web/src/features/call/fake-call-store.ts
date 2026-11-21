// 视觉原型阶段的通话状态：用定时器模拟对方接听、拒绝、超时与 WebRTC 协商。
// 第 8 阶段由 realtime/ 的通话信令与 RTCPeerConnection 取代。

import { create } from "zustand"
import type { ChatUser } from "@/features/chat/types"
import { inviteErrorText, type CallEndReason, type CallMedia } from "./call-text"

export type CallPhase =
  // 已收到 CallAck，对方振铃中
  | { kind: "outgoing"; since: number }
  // 收到 CallIncoming，本端振铃中
  | { kind: "incoming"; since: number }
  // 已接听，WebRTC 协商中
  | { kind: "connecting" }
  | { kind: "active"; since: number }
  | { kind: "ended"; reason: CallEndReason; duration?: number }

export interface Call {
  id: string
  peer: ChatUser
  media: CallMedia
  direction: "outgoing" | "incoming"
  phase: CallPhase
  // 是否接通过，决定结束文案
  connected: boolean
  muted: boolean
  cameraOff: boolean
  minimized: boolean
}

// 原型面板设置的「下一次呼叫的结果」
export type OutgoingOutcome = "accept" | "reject" | "busy" | "offline" | "timeout"

// 原型阶段压缩时间：对方 3 秒后接听，无人接听 8 秒后结束（实际为 60 秒）
const ANSWER_DELAY_MS = 3000
const DEMO_RING_TIMEOUT_MS = 8000
const CONNECT_DELAY_MS = 1200
// 结束画面停留时间，让用户看清结束原因
const ENDED_HOLD_MS = 1800

interface CallStore {
  call: Call | null
  nextOutcome: OutgoingOutcome
  // 呼叫请求被拒绝时的提示（通话没有开始），由界面显示后清除
  inviteError: { text: string; at: number } | null

  startCall: (peer: ChatUser, media: CallMedia) => void
  receiveCall: (peer: ChatUser, media: CallMedia) => void
  accept: () => void
  reject: () => void
  hangup: () => void
  remoteEnd: (reason: CallEndReason) => void
  toggleMute: () => void
  toggleCamera: () => void
  setMinimized: (minimized: boolean) => void
  setNextOutcome: (outcome: OutgoingOutcome) => void
  clearInviteError: () => void
}

let timers: ReturnType<typeof setTimeout>[] = []
function clearTimers() {
  timers.forEach(clearTimeout)
  timers = []
}
function later(ms: number, fn: () => void) {
  timers.push(setTimeout(fn, ms))
}

export const useCallStore = create<CallStore>()((set, get) => {
  const patchCall = (patch: Partial<Call>) => {
    const call = get().call
    if (call) set({ call: { ...call, ...patch } })
  }

  const connect = () => {
    patchCall({ phase: { kind: "connecting" } })
    later(CONNECT_DELAY_MS, () => patchCall({ phase: { kind: "active", since: Date.now() }, connected: true }))
  }

  const end = (reason: CallEndReason) => {
    const call = get().call
    if (!call || call.phase.kind === "ended") return
    clearTimers()
    const duration = call.phase.kind === "active" ? Date.now() - call.phase.since : undefined
    // 结束时展开悬浮窗，让用户看到结束原因
    patchCall({ phase: { kind: "ended", reason, duration }, minimized: false })
    later(ENDED_HOLD_MS, () => set({ call: null }))
  }

  const begin = (peer: ChatUser, media: CallMedia, direction: Call["direction"]) => {
    clearTimers()
    const since = Date.now()
    set({
      call: {
        id: crypto.randomUUID(),
        peer,
        media,
        direction,
        phase: direction === "outgoing" ? { kind: "outgoing", since } : { kind: "incoming", since },
        connected: false,
        muted: false,
        cameraOff: false,
        minimized: false,
      },
    })
  }

  return {
    call: null,
    nextOutcome: "accept",
    inviteError: null,

    startCall: (peer, media) => {
      // 一人一通：自己已在通话中时，界面上的呼叫按钮是禁用的，这里再兜底一次
      if (get().call) return
      const outcome = get().nextOutcome
      set({ nextOutcome: "accept" })
      // 忙线、离线在邀请时就返回 Error，不进入振铃
      if (outcome === "busy" || outcome === "offline") {
        set({ inviteError: { text: inviteErrorText(outcome === "busy" ? "CALL_BUSY" : "CALL_PEER_OFFLINE"), at: Date.now() } })
        return
      }
      begin(peer, media, "outgoing")
      if (outcome === "accept") later(ANSWER_DELAY_MS, connect)
      if (outcome === "reject") later(ANSWER_DELAY_MS, () => end("rejected"))
      if (outcome === "timeout") later(DEMO_RING_TIMEOUT_MS, () => end("timeout"))
    },

    receiveCall: (peer, media) => {
      if (get().call) return
      begin(peer, media, "incoming")
      later(DEMO_RING_TIMEOUT_MS, () => end("timeout"))
    },

    accept: () => {
      if (get().call?.phase.kind !== "incoming") return
      clearTimers()
      connect()
    },

    reject: () => end("rejected"),
    hangup: () => end("local-hangup"),
    remoteEnd: (reason) => end(reason),
    toggleMute: () => patchCall({ muted: !get().call?.muted }),
    toggleCamera: () => patchCall({ cameraOff: !get().call?.cameraOff }),
    setMinimized: (minimized) => patchCall({ minimized }),
    setNextOutcome: (outcome) => set({ nextOutcome: outcome }),
    clearInviteError: () => set({ inviteError: null }),
  }
})
