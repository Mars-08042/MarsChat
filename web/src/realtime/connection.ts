// WebSocket 连接状态机（设计 3.9.1、3.9.2；data-flow 第 3、7 节）：
// 打开后立即发 Auth；AuthOk 后按服务端给的间隔发 Ping，连续 2 个心跳周期收不到任何帧就判定失联；
// 按关闭码决定刷新令牌、重连还是停在被踢状态；重连按 backoff.ts 退避。
// 不依赖 React 和浏览器：WebSocket 由 openSocket 注入，单元测试用假的 socket 和假定时器。

import type { AuthOk, ServerFrame } from "@/gen/access/v1/frame_pb"
import { RevokeReason } from "@/gen/account/v1/events_pb"
import { reconnectDelay } from "./backoff"
import { decodeServerFrame, encodeClientFrame, type ClientBody } from "./codec"

export const CLOSE_UNAUTHORIZED = 4001
export const CLOSE_KICKED = 4002
export const CLOSE_GOING_AWAY = 1001
// 客户端判定失联时主动关闭用的关闭码；3000～4999 留给应用，后端没有用 4000
export const CLOSE_HEARTBEAT_TIMEOUT = 4000
// 后端返回的关闭码里，这两个说明前端发送的帧有问题（频率超限、帧太大），正常情况下不应出现
const CLIENT_BUG_CODES = new Set([4008, 1009])
// AuthOk 没给心跳间隔时的默认值，与后端一致
const DEFAULT_HEARTBEAT_SECONDS = 30
// 连续这么多个心跳周期没有收到任何帧，判定服务端失联
const MISSED_HEARTBEATS = 2

export interface SocketHandlers {
  open: () => void
  message: (data: ArrayBuffer | Uint8Array) => void
  close: (code: number, reason: string) => void
}

export interface Socket {
  send: (data: Uint8Array<ArrayBuffer>) => void
  close: (code: number, reason: string) => void
}

export type OpenSocket = (handlers: SocketHandlers) => Socket

export type ConnectionState =
  | { kind: "idle" }
  // 正在建立连接或等待 AuthOk
  | { kind: "connecting" }
  | { kind: "open"; auth: AuthOk }
  // 等待重连；maintenance 表示上一次是服务停机（1001）断开的
  | { kind: "waiting"; retryAt: number; maintenance: boolean }
  // 浏览器离线，收到 online 事件后再连
  | { kind: "offline" }
  // 被踢下线（4002），不再重连
  | { kind: "kicked"; reason: RevokeReason }

export interface ConnectionLogger {
  warn: (message: string, detail?: Record<string, unknown>) => void
  error: (message: string, detail?: Record<string, unknown>) => void
}

export interface ConnectionDeps {
  openSocket: OpenSocket
  // 取可用的访问令牌，快过期时会先刷新（tokens.accessToken）
  accessToken: () => Promise<string>
  // 收到 4001 后用被拒绝的令牌换新令牌（tokens.refresh）
  refresh: (stale: string) => Promise<string>
  isOnline?: () => boolean
  random?: () => number
  now?: () => number
  onState: (state: ConnectionState) => void
  // AuthOk、Pong 以外的帧
  onFrame: (frame: ServerFrame) => void
  // token 是这条连接认证时用的访问令牌，调用方据此判断本地是否已经换了会话
  onKicked: (reason: RevokeReason, token: string) => void
  log?: ConnectionLogger
}

export interface Connection {
  start(): void
  stop(): void
  // 立即重连（点击连接状态胶囊、收到 online 事件）；已连接或正在连接时什么也不做
  retryNow(): void
  setOnline(online: boolean): void
  // 已认证时发送并返回 request_id，否则返回 null
  send(body: ClientBody): bigint | null
  state(): ConnectionState
}

export function createConnection(deps: ConnectionDeps): Connection {
  const now = deps.now ?? Date.now
  const random = deps.random ?? Math.random
  const isOnline = deps.isOnline ?? (() => true)

  let state: ConnectionState = { kind: "idle" }
  let socket: Socket | null = null
  // 每次建立连接加一，旧连接迟到的事件按代数丢弃
  let generation = 0
  let failures = 0
  let retryTimer: ReturnType<typeof setTimeout> | undefined
  let heartbeatTimer: ReturnType<typeof setInterval> | undefined
  let lastFrameAt = 0
  // request_id 在整个页面生命周期内递增，不随重连归零，迟到的确认不会和新请求混淆
  let nextRequestId = 1n
  let tokenUsed = ""
  let kickedReason: RevokeReason | null = null

  const setState = (s: ConnectionState) => {
    state = s
    deps.onState(s)
  }

  const clearTimers = () => {
    clearTimeout(retryTimer)
    clearInterval(heartbeatTimer)
    retryTimer = undefined
    heartbeatTimer = undefined
  }

  const write = (requestId: bigint, body: ClientBody) => {
    socket?.send(encodeClientFrame({ requestId, body }))
  }

  // 丢弃当前连接：之后它的事件都会被忽略
  const drop = (code: number, reason: string) => {
    generation++
    const s = socket
    socket = null
    clearTimers()
    try {
      s?.close(code, reason)
    } catch {
      // 已经关闭的 socket 再关闭会抛错，忽略
    }
  }

  const scheduleReconnect = (maintenance = false) => {
    clearTimers()
    if (!isOnline()) {
      setState({ kind: "offline" })
      return
    }
    const delay = reconnectDelay(failures, random)
    failures++
    setState({ kind: "waiting", retryAt: now() + delay, maintenance })
    retryTimer = setTimeout(connect, delay)
  }

  const connect = () => {
    clearTimers()
    if (!isOnline()) {
      setState({ kind: "offline" })
      return
    }
    const gen = ++generation
    kickedReason = null
    setState({ kind: "connecting" })
    socket = deps.openSocket({
      open: () => {
        if (gen === generation) void authenticate(gen)
      },
      message: (data) => {
        if (gen === generation) receive(data)
      },
      close: (code, reason) => {
        if (gen === generation) closed(code, reason)
      },
    })
  }

  const authenticate = async (gen: number) => {
    let token: string
    try {
      token = await deps.accessToken()
    } catch (e) {
      if (gen !== generation) return
      // 登录状态已结束时调用方会 stop；网络问题导致刷新失败则按失败重连
      deps.log?.warn("access token unavailable", { error: String(e) })
      drop(CLOSE_HEARTBEAT_TIMEOUT, "token unavailable")
      scheduleReconnect()
      return
    }
    if (gen !== generation) return
    tokenUsed = token
    write(0n, { case: "auth", value: { accessToken: token } })
  }

  const startHeartbeat = (seconds: number) => {
    const interval = (seconds > 0 ? seconds : DEFAULT_HEARTBEAT_SECONDS) * 1000
    heartbeatTimer = setInterval(() => {
      if (now() - lastFrameAt >= interval * MISSED_HEARTBEATS) {
        deps.log?.warn("heartbeat timeout, reconnecting")
        drop(CLOSE_HEARTBEAT_TIMEOUT, "heartbeat timeout")
        scheduleReconnect()
        return
      }
      write(0n, { case: "ping", value: {} })
    }, interval)
  }

  const receive = (data: ArrayBuffer | Uint8Array) => {
    let frame: ServerFrame
    try {
      frame = decodeServerFrame(data)
    } catch (e) {
      deps.log?.error("undecodable server frame", { error: String(e) })
      return
    }
    lastFrameAt = now()
    switch (frame.body.case) {
      case "authOk":
        failures = 0
        setState({ kind: "open", auth: frame.body.value })
        startHeartbeat(frame.body.value.heartbeatIntervalSeconds)
        return
      case "pong":
        return
      case "kicked":
        // 随后以 4002 关闭，在 closed 里处理
        kickedReason = frame.body.value.reason
        return
      default:
        deps.onFrame(frame)
    }
  }

  const closed = (code: number, reason: string) => {
    socket = null
    clearTimers()
    if (code === CLOSE_KICKED) {
      const r = kickedReason ?? RevokeReason.UNSPECIFIED
      setState({ kind: "kicked", reason: r })
      deps.onKicked(r, tokenUsed)
      return
    }
    if (code === CLOSE_UNAUTHORIZED) {
      // 令牌被拒绝：刷新后再连。刷新失败时登录状态由令牌管理结束，调用方随之 stop。
      // 刷新成功后也按退避等待，服务端持续拒绝时不会陷入「刷新 → 连接 → 4001」的快速循环
      const gen = generation
      const stale = tokenUsed
      setState({ kind: "connecting" })
      deps.refresh(stale).then(
        () => {
          if (gen === generation) scheduleReconnect()
        },
        (e: unknown) => {
          if (gen !== generation) return
          deps.log?.warn("refresh after 4001 failed", { error: String(e) })
          scheduleReconnect()
        },
      )
      return
    }
    if (CLIENT_BUG_CODES.has(code)) deps.log?.error("connection closed by server due to client frames", { code, reason })
    scheduleReconnect(code === CLOSE_GOING_AWAY)
  }

  return {
    start: () => {
      if (state.kind !== "idle") return
      failures = 0
      connect()
    },
    stop: () => {
      drop(1000, "client stop")
      setState({ kind: "idle" })
    },
    retryNow: () => {
      if (state.kind === "waiting" || state.kind === "offline") connect()
    },
    setOnline: (online) => {
      if (online) {
        if (state.kind === "waiting" || state.kind === "offline") connect()
        return
      }
      // 离线时不必等重连定时器空转；已连接的由心跳发现失联
      if (state.kind === "waiting") {
        clearTimers()
        setState({ kind: "offline" })
      }
    },
    send: (body) => {
      if (state.kind !== "open" || !socket) return null
      const id = nextRequestId++
      write(id, body)
      return id
    },
    state: () => state,
  }
}
