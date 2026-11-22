import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { create } from "@bufbuild/protobuf"
import { ErrorSchema } from "@/gen/access/v1/frame_pb"
import { ConversationType, SendResultSchema } from "@/gen/message/v1/message_pb"
import type { ChatMessage } from "@/features/chat/types"
import type { ClientBody } from "./codec"
import { ACK_TIMEOUT_MS, createSendQueue, MAX_AUTO_RETRIES, MAX_IN_FLIGHT, SEND_FRAMES_PER_SECOND, type Outgoing } from "./send-queue"

const message = (n: number): ChatMessage => ({
  id: `1:m${n}`,
  conversationId: "100",
  senderId: "1",
  content: { kind: "text", text: `text ${n}` },
  sentAt: 0,
  status: "pending",
})

const outgoing = (n: number): Outgoing => ({
  peerType: ConversationType.SINGLE,
  peerId: 2n,
  clientMsgId: `m${n}`,
  content: { body: { case: "text", value: { text: `text ${n}` } } },
})

const error = (reason: string, code = 503, metadata: Record<string, string> = {}) =>
  create(ErrorSchema, { code, reason, message: reason, metadata })

const ack = (seq: bigint) => create(SendResultSchema, { conversationId: 100n, seq })

function setup() {
  let open = true
  let nextId = 1n
  const sent: Array<{ id: bigint; clientMsgId: string }> = []
  const updates: ChatMessage[] = []
  const acked: Array<{ message: ChatMessage; seq: bigint }> = []
  const forbidden: Array<[string, string]> = []
  const rateLimited: Array<number | undefined> = []
  const queue = createSendQueue({
    send: (body: ClientBody) => {
      if (!open || body?.case !== "sendMessage") return null
      const id = nextId++
      sent.push({ id, clientMsgId: body.value.clientMsgId ?? "" })
      return id
    },
    onUpdate: (m) => updates.push(m),
    onAck: (m, a) => acked.push({ message: m, seq: a.seq }),
    onForbidden: (m, reason) => forbidden.push([m.id, reason]),
    onRateLimited: (until) => rateLimited.push(until),
  })
  const setOpen = (v: boolean) => {
    open = v
  }
  const lastUpdate = (id: string) => updates.filter((u) => u.id === id).at(-1)
  const lastRequest = (clientMsgId: string) => sent.filter((s) => s.clientMsgId === clientMsgId).at(-1)!.id
  return { queue, sent, updates, acked, forbidden, rateLimited, setOpen, lastUpdate, lastRequest }
}

beforeEach(() => {
  vi.useFakeTimers()
})
afterEach(() => {
  vi.useRealTimers()
})

describe("发送与确认", () => {
  it("入队后立即显示为发送中并发送；收到确认后交给同步引擎", () => {
    const t = setup()
    t.queue.enqueue(message(1), outgoing(1))
    expect(t.lastUpdate("1:m1")?.status).toBe("pending")
    expect(t.sent).toEqual([{ id: 1n, clientMsgId: "m1" }])
    expect(t.queue.handleAck(1n, ack(7n))).toBe(true)
    expect(t.acked).toEqual([{ message: expect.objectContaining({ id: "1:m1" }), seq: 7n }])
    expect(t.queue.hasUnconfirmed()).toBe(false)
  })

  it("未连接时排队，连接后按入队顺序发送", () => {
    const t = setup()
    t.setOpen(false)
    t.queue.enqueue(message(1), outgoing(1))
    t.queue.enqueue(message(2), outgoing(2))
    expect(t.sent).toEqual([])
    expect(t.queue.hasUnconfirmed()).toBe(true)
    t.setOpen(true)
    t.queue.connected()
    expect(t.sent.map((s) => s.clientMsgId)).toEqual(["m1", "m2"])
  })

  it("不认识的 request_id 不处理", () => {
    const t = setup()
    expect(t.queue.handleAck(99n, ack(1n))).toBe(false)
    expect(t.queue.handleError(99n, error("RATE_LIMITED", 429))).toBe(false)
  })
})

describe("发送速度", () => {
  // 按发送顺序确认所有已发出的消息；确认会腾出名额，期间发出的消息也一并确认
  const ackAll = (t: ReturnType<typeof setup>) => {
    for (let i = 0; i < t.sent.length; i++) t.queue.handleAck(t.sent[i].id, ack(BigInt(i + 1)))
  }

  it(`最多 ${MAX_IN_FLIGHT} 条同时等待确认，收到确认后继续发送`, () => {
    const t = setup()
    for (let i = 1; i <= MAX_IN_FLIGHT + 3; i++) t.queue.enqueue(message(i), outgoing(i))
    expect(t.sent).toHaveLength(MAX_IN_FLIGHT)
    t.queue.handleAck(t.sent[0].id, ack(1n))
    expect(t.sent).toHaveLength(MAX_IN_FLIGHT + 1)
  })

  it("重连后不会一次发出全部积压的消息", () => {
    const t = setup()
    t.setOpen(false)
    for (let i = 1; i <= 12; i++) t.queue.enqueue(message(i), outgoing(i))
    t.setOpen(true)
    t.queue.connected()
    expect(t.sent.map((s) => s.clientMsgId)).toEqual(["m1", "m2", "m3", "m4", "m5"])
  })

  it(`每秒最多发出 ${SEND_FRAMES_PER_SECOND} 条，其余等窗口滑过后发送`, async () => {
    const t = setup()
    const total = SEND_FRAMES_PER_SECOND + 10
    for (let i = 1; i <= total; i++) t.queue.enqueue(message(i), outgoing(i))
    ackAll(t)
    expect(t.sent).toHaveLength(SEND_FRAMES_PER_SECOND)
    await vi.advanceTimersByTimeAsync(999)
    expect(t.sent).toHaveLength(SEND_FRAMES_PER_SECOND)
    await vi.advanceTimersByTimeAsync(1)
    ackAll(t)
    expect(t.sent.map((s) => s.clientMsgId)).toEqual(Array.from({ length: total }, (_, i) => `m${i + 1}`))
    expect(t.queue.hasUnconfirmed()).toBe(false)
  })
})

describe("结果未知时自动重发", () => {
  it("断线：重连后用同一个 client_msg_id 重发", () => {
    const t = setup()
    t.queue.enqueue(message(1), outgoing(1))
    t.setOpen(false)
    t.queue.disconnected()
    t.setOpen(true)
    t.queue.connected()
    expect(t.sent.map((s) => s.clientMsgId)).toEqual(["m1", "m1"])
    // 第一次发送的确认迟到了也算成功
    expect(t.queue.handleAck(1n, ack(3n))).toBe(true)
    expect(t.acked).toHaveLength(1)
  })

  it("超过 10 秒没有确认：重发", async () => {
    const t = setup()
    t.queue.enqueue(message(1), outgoing(1))
    await vi.advanceTimersByTimeAsync(ACK_TIMEOUT_MS)
    expect(t.sent.map((s) => s.clientMsgId)).toEqual(["m1", "m1"])
  })

  it(`最多自动重发 ${MAX_AUTO_RETRIES} 次，之后标为失败`, async () => {
    const t = setup()
    t.queue.enqueue(message(1), outgoing(1))
    for (let i = 0; i < MAX_AUTO_RETRIES + 1; i++) await vi.advanceTimersByTimeAsync(ACK_TIMEOUT_MS)
    expect(t.sent).toHaveLength(1 + MAX_AUTO_RETRIES)
    expect(t.lastUpdate("1:m1")).toMatchObject({ status: "failed", failReason: "发送超时，请重试" })
    expect(t.queue.hasUnconfirmed()).toBe(false)
  })

  it.each(["UPSTREAM_UNAVAILABLE", "CIRCUITBREAKER", "DEPENDENCY_UNAVAILABLE"])("%s：等待后重发，等待时间逐次增加", async (reason) => {
    const t = setup()
    t.queue.enqueue(message(1), outgoing(1))
    t.queue.handleError(t.lastRequest("m1"), error(reason))
    await vi.advanceTimersByTimeAsync(999)
    expect(t.sent).toHaveLength(1)
    await vi.advanceTimersByTimeAsync(1)
    expect(t.sent).toHaveLength(2)
    t.queue.handleError(t.lastRequest("m1"), error(reason))
    await vi.advanceTimersByTimeAsync(1999)
    expect(t.sent).toHaveLength(2)
    await vi.advanceTimersByTimeAsync(1)
    expect(t.sent).toHaveLength(3)
  })

  it("手动重试：次数清零，重新发送", async () => {
    const t = setup()
    t.queue.enqueue(message(1), outgoing(1))
    for (let i = 0; i < MAX_AUTO_RETRIES + 1; i++) await vi.advanceTimersByTimeAsync(ACK_TIMEOUT_MS)
    t.queue.retry("1:m1")
    expect(t.lastUpdate("1:m1")).toMatchObject({ status: "pending", failReason: undefined })
    expect(t.sent).toHaveLength(2 + MAX_AUTO_RETRIES)
    t.queue.handleAck(t.lastRequest("m1"), ack(1n))
    expect(t.acked).toHaveLength(1)
  })
})

describe("限流与拒绝", () => {
  it("RATE_LIMITED：等待 retry_after_seconds 后重发，不计重试次数", async () => {
    const t = setup()
    t.queue.enqueue(message(1), outgoing(1))
    const start = Date.now()
    t.queue.handleError(t.lastRequest("m1"), error("RATE_LIMITED", 429, { retry_after_seconds: "4" }))
    expect(t.rateLimited).toEqual([start + 4000])
    expect(t.lastUpdate("1:m1")?.status).toBe("pending")
    await vi.advanceTimersByTimeAsync(3999)
    expect(t.sent).toHaveLength(1)
    await vi.advanceTimersByTimeAsync(1)
    expect(t.sent).toHaveLength(2)
    expect(t.rateLimited.at(-1)).toBeUndefined()

    // 连续被限流多次也不会标为失败
    for (let i = 0; i < MAX_AUTO_RETRIES + 2; i++) {
      t.queue.handleError(t.lastRequest("m1"), error("RATE_LIMITED", 429, { retry_after_seconds: "1" }))
      await vi.advanceTimersByTimeAsync(1000)
    }
    expect(t.lastUpdate("1:m1")?.status).toBe("pending")
  })

  it.each([
    ["NOT_FRIENDS", "你们还不是好友，不能发送消息"],
    ["GROUP_MUTED", "群已开启全员禁言，只有群主和管理员可以发言"],
    ["MEMBER_MUTED", "你已被禁言"],
  ])("%s：不重试，标为失败并通知会话不能发言", async (reason, text) => {
    const t = setup()
    t.queue.enqueue(message(1), outgoing(1))
    t.queue.handleError(t.lastRequest("m1"), error(reason, 403))
    expect(t.lastUpdate("1:m1")).toMatchObject({ status: "failed", failReason: text })
    expect(t.forbidden).toEqual([["1:m1", reason]])
    await vi.advanceTimersByTimeAsync(60_000)
    expect(t.sent).toHaveLength(1)
  })

  it("VALIDATOR：标为失败，不影响会话", () => {
    const t = setup()
    t.queue.enqueue(message(1), outgoing(1))
    t.queue.handleError(t.lastRequest("m1"), error("VALIDATOR", 400))
    expect(t.lastUpdate("1:m1")).toMatchObject({ status: "failed", failReason: "填写的内容格式不正确" })
    expect(t.forbidden).toEqual([])
  })

  it("被限流时整个队列暂停，结束后按原来的顺序重发", async () => {
    const t = setup()
    for (let i = 1; i <= 3; i++) t.queue.enqueue(message(i), outgoing(i))
    t.queue.handleError(t.lastRequest("m1"), error("RATE_LIMITED", 429, { retry_after_seconds: "2" }))
    t.queue.handleError(t.lastRequest("m2"), error("RATE_LIMITED", 429, { retry_after_seconds: "2" }))
    t.queue.enqueue(message(4), outgoing(4))
    expect(t.sent.map((s) => s.clientMsgId)).toEqual(["m1", "m2", "m3"])
    await vi.advanceTimersByTimeAsync(2000)
    expect(t.sent.map((s) => s.clientMsgId)).toEqual(["m1", "m2", "m3", "m1", "m2", "m4"])
  })

  it("失败的消息不影响后面的消息发送", () => {
    const t = setup()
    t.queue.enqueue(message(1), outgoing(1))
    t.queue.handleError(t.lastRequest("m1"), error("VALIDATOR", 400))
    t.queue.enqueue(message(2), outgoing(2))
    expect(t.sent.map((s) => s.clientMsgId)).toEqual(["m1", "m2"])
  })
})
