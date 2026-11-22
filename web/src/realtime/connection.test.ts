import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import type { MessageInitShape } from "@bufbuild/protobuf"
import type { ClientFrame, ServerFrame, ServerFrameSchema } from "@/gen/access/v1/frame_pb"
import { RevokeReason } from "@/gen/account/v1/events_pb"
import { ConversationType } from "@/gen/message/v1/message_pb"
import { decodeClientFrame, encodeServerFrame } from "./codec"
import { createConnection, type ConnectionState, type SocketHandlers } from "./connection"

class FakeSocket {
  sent: ClientFrame[] = []
  closedWith: { code: number; reason: string } | null = null
  constructor(readonly handlers: SocketHandlers) {}
  readonly api = {
    send: (data: Uint8Array) => this.sent.push(decodeClientFrame(data)),
    close: (code: number, reason: string) => {
      this.closedWith = { code, reason }
    },
  }
  open() {
    this.handlers.open()
  }
  push(frame: MessageInitShape<typeof ServerFrameSchema>) {
    this.handlers.message(encodeServerFrame(frame))
  }
  serverClose(code: number, reason = "") {
    this.handlers.close(code, reason)
  }
  bodies() {
    return this.sent.map((f) => f.body.case)
  }
}

const authOk = (heartbeatIntervalSeconds = 30): MessageInitShape<typeof ServerFrameSchema> => ({
  body: { case: "authOk", value: { userId: 42n, sessionId: 7n, connectionId: 99n, heartbeatIntervalSeconds } },
})

function harness() {
  const sockets: FakeSocket[] = []
  const states: ConnectionState[] = []
  const frames: ServerFrame[] = []
  const kicked: Array<[RevokeReason, string]> = []
  let online = true
  let token = "a1"
  const accessToken = vi.fn(async () => token)
  const refresh = vi.fn(async () => {
    token = "a2"
    return token
  })
  const log = { warn: vi.fn(), error: vi.fn() }
  const conn = createConnection({
    openSocket: (h) => {
      const s = new FakeSocket(h)
      sockets.push(s)
      return s.api
    },
    accessToken,
    refresh,
    isOnline: () => online,
    // 抖动固定为 1 秒，便于断言
    random: () => 0.5,
    onState: (s) => states.push(s),
    onFrame: (f) => frames.push(f),
    onKicked: (r, t) => kicked.push([r, t]),
    log,
  })
  const last = () => sockets[sockets.length - 1]
  const state = () => conn.state()
  const setOnline = (v: boolean) => {
    online = v
  }
  // 打开最新的 socket 并完成认证
  const connectOk = async (heartbeat = 30) => {
    last().open()
    await vi.advanceTimersByTimeAsync(0)
    last().push(authOk(heartbeat))
  }
  return { conn, sockets, states, frames, kicked, accessToken, refresh, log, last, state, setOnline, connectOk }
}

// 处于等待重连状态时，距离重连还有多少毫秒
const waitMs = (s: ConnectionState) => (s.kind === "waiting" ? s.retryAt - Date.now() : NaN)

beforeEach(() => {
  vi.useFakeTimers()
})
afterEach(() => {
  vi.useRealTimers()
})

describe("认证与心跳", () => {
  it("打开后立即发送 Auth，收到 AuthOk 后进入 open", async () => {
    const h = harness()
    h.conn.start()
    expect(h.state().kind).toBe("connecting")
    h.last().open()
    await vi.advanceTimersByTimeAsync(0)
    expect(h.last().sent[0].body).toEqual({ case: "auth", value: expect.objectContaining({ accessToken: "a1" }) })

    h.last().push(authOk())
    expect(h.state()).toMatchObject({ kind: "open", auth: { userId: 42n, connectionId: 99n } })
  })

  it("按 AuthOk 给的间隔发送 Ping", async () => {
    const h = harness()
    h.conn.start()
    await h.connectOk(10)
    await vi.advanceTimersByTimeAsync(10_000)
    expect(h.last().bodies()).toEqual(["auth", "ping"])
    h.last().push({ body: { case: "pong", value: {} } })
    await vi.advanceTimersByTimeAsync(10_000)
    expect(h.last().bodies()).toEqual(["auth", "ping", "ping"])
    expect(h.frames).toEqual([])
  })

  it("连续 2 个心跳周期没有收到任何帧：主动关闭并重连", async () => {
    const h = harness()
    h.conn.start()
    await h.connectOk(30)
    await vi.advanceTimersByTimeAsync(30_000)
    expect(h.state().kind).toBe("open")
    await vi.advanceTimersByTimeAsync(30_000)
    expect(h.sockets[0].closedWith).toEqual({ code: 4000, reason: "heartbeat timeout" })
    // 刚才还在线，失败计数为 0，只等随机抖动
    expect(waitMs(h.state())).toBe(1000)
    await vi.advanceTimersByTimeAsync(1000)
    expect(h.sockets).toHaveLength(2)
  })

  it("收到的任何帧都算存活", async () => {
    const h = harness()
    h.conn.start()
    await h.connectOk(30)
    await vi.advanceTimersByTimeAsync(50_000)
    h.last().push({ body: { case: "read", value: { conversationId: 1n, userId: 2n, readSeq: 3n } } })
    await vi.advanceTimersByTimeAsync(20_000)
    expect(h.state().kind).toBe("open")
    expect(h.frames.map((f) => f.body.case)).toEqual(["read"])
  })

  it("取访问令牌失败：放弃这次连接，按失败重连", async () => {
    const h = harness()
    h.accessToken.mockRejectedValueOnce(new Error("network"))
    h.conn.start()
    h.last().open()
    await vi.advanceTimersByTimeAsync(0)
    expect(h.sockets[0].closedWith?.code).toBe(4000)
    expect(h.state().kind).toBe("waiting")
    await vi.advanceTimersByTimeAsync(1000)
    await h.connectOk()
    expect(h.state().kind).toBe("open")
  })
})

describe("关闭码与重连", () => {
  it("1001 服务停机：标记维护中，随机等待 0～2 秒后重连", async () => {
    const h = harness()
    h.conn.start()
    await h.connectOk()
    h.last().serverClose(1001, "server shutting down")
    expect(h.state()).toMatchObject({ kind: "waiting", maintenance: true })
    expect(waitMs(h.state())).toBe(1000)
    await vi.advanceTimersByTimeAsync(999)
    expect(h.sockets).toHaveLength(1)
    await vi.advanceTimersByTimeAsync(1)
    expect(h.sockets).toHaveLength(2)
  })

  it("连续失败按 1、2、4、8 秒……指数退避，最长 30 秒，再加抖动", async () => {
    const h = harness()
    h.conn.start()
    const delays: number[] = []
    for (let i = 0; i < 8; i++) {
      // 连接被拒绝时浏览器只触发 close（1006）
      h.last().serverClose(1006)
      delays.push(waitMs(h.state()))
      await vi.advanceTimersByTimeAsync(waitMs(h.state()))
    }
    expect(delays).toEqual([1000, 2000, 3000, 5000, 9000, 17_000, 31_000, 31_000])
  })

  it("认证成功后失败计数清零", async () => {
    const h = harness()
    h.conn.start()
    h.last().serverClose(1006)
    await vi.advanceTimersByTimeAsync(1000)
    h.last().serverClose(1006)
    await vi.advanceTimersByTimeAsync(2000)
    await h.connectOk()
    h.last().serverClose(1006)
    expect(waitMs(h.state())).toBe(1000)
  })

  it("4001：用被拒绝的令牌刷新后重连，新连接使用新令牌", async () => {
    const h = harness()
    h.conn.start()
    h.last().open()
    await vi.advanceTimersByTimeAsync(0)
    h.last().push({ body: { case: "error", value: { code: 401, reason: "UNAUTHORIZED" } } })
    h.last().serverClose(4001)
    await vi.advanceTimersByTimeAsync(0)
    expect(h.refresh).toHaveBeenCalledExactlyOnceWith("a1")
    await vi.advanceTimersByTimeAsync(1000)
    expect(h.sockets).toHaveLength(2)
    h.last().open()
    await vi.advanceTimersByTimeAsync(0)
    expect(h.last().sent[0].body).toEqual({ case: "auth", value: expect.objectContaining({ accessToken: "a2" }) })
  })

  it("4001 后刷新失败：按退避重连", async () => {
    const h = harness()
    h.refresh.mockRejectedValueOnce(new Error("network"))
    h.conn.start()
    h.last().open()
    await vi.advanceTimersByTimeAsync(0)
    h.last().serverClose(4001)
    await vi.advanceTimersByTimeAsync(0)
    expect(h.state().kind).toBe("waiting")
  })

  it("4002：按 Kicked 的原因停在被踢状态，不再重连", async () => {
    const h = harness()
    h.conn.start()
    await h.connectOk()
    h.last().push({ body: { case: "kicked", value: { reason: RevokeReason.BANNED } } })
    h.last().serverClose(4002)
    expect(h.state()).toEqual({ kind: "kicked", reason: RevokeReason.BANNED })
    expect(h.kicked).toEqual([[RevokeReason.BANNED, "a1"]])
    await vi.advanceTimersByTimeAsync(120_000)
    expect(h.sockets).toHaveLength(1)
    // Kicked 帧不交给业务层
    expect(h.frames).toEqual([])
  })

  it("4002 之前没收到 Kicked：原因按未指定处理", async () => {
    const h = harness()
    h.conn.start()
    await h.connectOk()
    h.last().serverClose(4002)
    expect(h.kicked).toEqual([[RevokeReason.UNSPECIFIED, "a1"]])
  })

  it("4008、1009 说明前端有 bug：记录错误后重连", async () => {
    const h = harness()
    h.conn.start()
    await h.connectOk()
    h.last().serverClose(4008, "too many frames")
    expect(h.log.error).toHaveBeenCalledOnce()
    expect(h.state().kind).toBe("waiting")
  })
})

describe("离线与手动重连", () => {
  it("离线时不重连，收到 online 后立即连接", async () => {
    const h = harness()
    h.conn.start()
    await h.connectOk()
    h.setOnline(false)
    h.last().serverClose(1006)
    expect(h.state().kind).toBe("offline")
    await vi.advanceTimersByTimeAsync(60_000)
    expect(h.sockets).toHaveLength(1)
    h.setOnline(true)
    h.conn.setOnline(true)
    expect(h.sockets).toHaveLength(2)
  })

  it("等待重连时收到 offline：取消定时器", async () => {
    const h = harness()
    h.conn.start()
    h.last().serverClose(1006)
    h.setOnline(false)
    h.conn.setOnline(false)
    expect(h.state().kind).toBe("offline")
    await vi.advanceTimersByTimeAsync(60_000)
    expect(h.sockets).toHaveLength(1)
  })

  it("等待重连时可以立即重连，已连接时什么也不做", async () => {
    const h = harness()
    h.conn.start()
    h.last().serverClose(1006)
    h.conn.retryNow()
    expect(h.sockets).toHaveLength(2)
    await h.connectOk()
    h.conn.retryNow()
    expect(h.sockets).toHaveLength(2)
  })

  it("stop：以 1000 关闭，旧连接迟到的事件被忽略", async () => {
    const h = harness()
    h.conn.start()
    await h.connectOk()
    const old = h.last()
    h.conn.stop()
    expect(old.closedWith?.code).toBe(1000)
    expect(h.state().kind).toBe("idle")
    old.serverClose(1006)
    old.push({ body: { case: "read", value: {} } })
    await vi.advanceTimersByTimeAsync(60_000)
    expect(h.sockets).toHaveLength(1)
    expect(h.state().kind).toBe("idle")
    expect(h.frames).toEqual([])
  })
})

describe("send", () => {
  it("未认证时不发送；认证后 request_id 递增，重连后继续递增", async () => {
    const h = harness()
    const msg = {
      case: "sendMessage" as const,
      value: { peerType: ConversationType.SINGLE, peerId: 5n, clientMsgId: "m1", content: { body: { case: "text" as const, value: { text: "hi" } } } },
    }
    h.conn.start()
    expect(h.conn.send(msg)).toBeNull()
    await h.connectOk()
    expect(h.conn.send(msg)).toBe(1n)
    expect(h.conn.send(msg)).toBe(2n)
    expect(h.last().sent.at(-1)).toMatchObject({ requestId: 2n, body: { case: "sendMessage" } })

    h.last().serverClose(1006)
    expect(h.conn.send(msg)).toBeNull()
    await vi.advanceTimersByTimeAsync(1000)
    await h.connectOk()
    expect(h.conn.send(msg)).toBe(3n)
  })
})
