// 通话的结束原因与呼叫错误到界面文案的映射（设计 3.6.10）。
// 原因对应 access.v1.CallEndReason 与 ErrorReason（call-signaling.md 4.1、4.2），另加两种只在客户端产生的原因。

export type CallMedia = "audio" | "video"

export type CallEndReason =
  // 服务端 CallEnded 的原因
  | "hangup"
  | "rejected"
  | "timeout"
  | "answered-elsewhere"
  | "disconnected"
  | "peer-offline"
  // 客户端自己产生：WebRTC 连接失败（没有 TURN 时跨网络可能无法直连），或本端主动结束
  | "media-failed"
  | "local-hangup"

// 谁看到这条文案：主叫和被叫对同一个原因的说法不同（例如 hangup 在振铃中表示主叫取消）
export interface EndContext {
  direction: "outgoing" | "incoming"
  // 是否已经接通过
  connected: boolean
}

export function endReasonText(reason: CallEndReason, ctx: EndContext): string {
  switch (reason) {
    case "hangup":
      if (ctx.connected) return "对方已挂断"
      return ctx.direction === "incoming" ? "对方已取消" : "对方已挂断"
    case "rejected":
      return ctx.direction === "outgoing" ? "对方拒绝了通话" : "已拒绝"
    case "timeout":
      return ctx.direction === "outgoing" ? "对方无人接听" : "未接来电"
    case "answered-elsewhere":
      return "已在其他设备接听"
    case "disconnected":
      return "连接已断开，通话结束"
    case "peer-offline":
      return "对方不在线"
    case "media-failed":
      return "网络无法直连，通话已结束"
    case "local-hangup":
      return ctx.connected ? "通话结束" : "已取消"
  }
}

// 呼叫请求被拒绝时（Error 帧，通话没有开始）的提示；未知原因给出通用文案
const INVITE_ERRORS: Record<string, string> = {
  CALL_BUSY: "对方正在通话中，请稍后再拨",
  ALREADY_IN_CALL: "你正在另一台设备上通话",
  CALL_PEER_OFFLINE: "对方不在线，暂时无法接通",
  NOT_FRIENDS: "你们还不是好友，无法通话",
  BLOCKED: "无法呼叫对方",
  UPSTREAM_UNAVAILABLE: "服务暂时不可用，请稍后重试",
}

export function inviteErrorText(reason: string): string {
  return INVITE_ERRORS[reason] ?? "呼叫失败，请稍后重试"
}

// 实时连接还没建立（断线重连中）时发起呼叫
export const NOT_CONNECTED_TEXT = "正在连接服务器，请稍后再拨"
// 浏览器没有可用的 RTCPeerConnection：太旧，或被防 IP 泄露的扩展禁用
export const WEBRTC_UNSUPPORTED_TEXT = "当前浏览器无法通话：WebRTC 不可用，可能被浏览器扩展禁用"
// 接听请求被拒绝但来电还在（例如服务暂时不可用），可以再点一次接听
export const ACCEPT_FAILED_TEXT = "接听失败，请再试一次"

// 媒体设备不可用时的提示，name 来自 getUserMedia 抛出的 DOMException
export function deviceErrorText(name: string, media: CallMedia): string {
  const device = media === "video" ? "摄像头或麦克风" : "麦克风"
  if (name === "NotAllowedError") return `没有${device}权限，请在浏览器地址栏的权限设置中允许`
  if (name === "NotFoundError") return `没有找到可用的${device}`
  if (name === "NotReadableError") return `${device}正被其他程序占用`
  return `无法使用${device}`
}

// 被叫客户端最多响铃 ring_timeout + 5 秒：主叫所在实例崩溃时没有人发 CallEnded（call.proto 注释）
export const RING_TIMEOUT_SECONDS = 60
export const CLIENT_RING_LIMIT_SECONDS = RING_TIMEOUT_SECONDS + 5
