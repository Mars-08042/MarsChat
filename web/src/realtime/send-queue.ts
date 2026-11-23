// 发送队列（设计 3.9.4；data-flow 第 4 节）：
// - 每条消息带客户端生成的 client_msg_id，重发时不变，服务端只存一条；
// - 连接可用时按入队顺序发送，同一条连接上的确认按发送顺序返回；
// - 控制发送速度：access 每个连接每秒最多收 30 个上行帧，超出直接断开（4008），
//   所以发消息每秒最多用 20 帧（给心跳和通话信令留余量），同时最多 5 条等待确认，重连后不会一次发出全部积压；
// - 结果未知或可以重试的错误（断线、10 秒没有确认、UPSTREAM_UNAVAILABLE、CIRCUITBREAKER、DEPENDENCY_UNAVAILABLE）
//   自动重发，最多 3 次；RATE_LIMITED 时整个队列暂停到 retry_after_seconds 之后，被限流的消息按原来的顺序重发，
//   不计次数；其余错误不重试，标为失败。
// 只存在内存里：刷新页面会丢掉还没确认的消息，由界面在离开前提示。

import type { MessageInitShape } from "@bufbuild/protobuf"
import type { Error as FrameError } from "@/gen/access/v1/frame_pb"
import type { OutgoingMessageSchema, SendResult } from "@/gen/message/v1/message_pb"
import { ApiError, errorText } from "@/api/errors"
import type { ChatMessage } from "@/features/chat/types"
import type { ClientBody } from "./codec"

export const ACK_TIMEOUT_MS = 10_000
export const MAX_AUTO_RETRIES = 3
// 可重试的错误连续出现时，第 n 次重发前等待 n 秒
const RETRY_STEP_MS = 1000
// RATE_LIMITED 没带等待时间时按这个值等待
const DEFAULT_RATE_LIMIT_MS = 5000
// 每个滑动窗口（1 秒）内最多发出的消息帧数，见文件开头
export const SEND_FRAMES_PER_SECOND = 20
const SEND_WINDOW_MS = 1000
// 同时等待确认的消息数上限
export const MAX_IN_FLIGHT = 5

const RETRYABLE = new Set(["UPSTREAM_UNAVAILABLE", "CIRCUITBREAKER", "DEPENDENCY_UNAVAILABLE"])
// 发言被拒：当前会话不能再发言，输入框换成说明条（设计 3.6.6）
export const SEND_FORBIDDEN = new Set([
  "NOT_FRIENDS",
  "BLOCKED",
  "NOT_GROUP_MEMBER",
  "MEMBER_MUTED",
  "GROUP_MUTED",
  "GROUP_DISMISSED",
])

export type Outgoing = MessageInitShape<typeof OutgoingMessageSchema>

export interface SendQueueDeps {
  // 已认证时发送并返回 request_id，否则返回 null（connection.send）
  send: (body: ClientBody) => bigint | null
  // 消息状态变化（发送中、失败），由调用方写回 outbox
  onUpdate: (message: ChatMessage) => void
  onAck: (message: ChatMessage, ack: SendResult) => void
  // 发言被拒，reason 是错误原因，text 是给用户看的说明
  onForbidden?: (message: ChatMessage, reason: string, text: string) => void
  // 被限流时自动重发的时间点；undefined 表示不再等待
  onRateLimited?: (until: number | undefined) => void
  now?: () => number
}

interface Item {
  message: ChatMessage
  outgoing: Outgoing
  // 已经自动重发的次数
  retries: number
  state: "queued" | "inflight" | "waiting" | "failed"
  // 这条消息每次发送用过的 request_id，任何一次的确认都算成功
  requestIds: bigint[]
  timer?: ReturnType<typeof setTimeout>
}

export interface SendQueue {
  enqueue(message: ChatMessage, outgoing: Outgoing): void
  // 连接认证成功后调用，按顺序发送排队中的消息
  connected(): void
  // 连接断开后调用：发出去还没确认的消息结果未知，计一次重试，重连后重发
  disconnected(): void
  handleAck(requestId: bigint, ack: SendResult): boolean
  handleError(requestId: bigint, error: FrameError): boolean
  // 手动重试失败的消息，重试次数清零
  retry(messageId: string): void
  // 还有没确认的消息（发送中、等待重发），离开页面前要提示
  hasUnconfirmed(): boolean
  clear(): void
}

export function createSendQueue(deps: SendQueueDeps): SendQueue {
  const now = deps.now ?? Date.now
  // Map 保持插入顺序，即发送顺序
  const items = new Map<string, Item>()
  let rateLimitedUntil: number | undefined
  // 最近一个窗口内每次发送的时间
  let sentAt: number[] = []
  let flushTimer: ReturnType<typeof setTimeout> | undefined

  const update = (item: Item, patch: Partial<ChatMessage>) => {
    item.message = { ...item.message, ...patch }
    deps.onUpdate(item.message)
  }

  const findByRequest = (requestId: bigint) => {
    for (const item of items.values()) if (item.requestIds.includes(requestId)) return item
    return undefined
  }

  const fail = (item: Item, reason: string) => {
    clearTimeout(item.timer)
    item.state = "failed"
    update(item, { status: "failed", failReason: reason })
  }

  // 结果未知或可重试：计一次重试，超过上限就标为失败
  const retryLater = (item: Item, delay: number, reason: string) => {
    clearTimeout(item.timer)
    item.retries++
    if (item.retries > MAX_AUTO_RETRIES) {
      fail(item, reason)
      return
    }
    if (delay === 0) {
      item.state = "queued"
      flush()
      return
    }
    item.state = "waiting"
    item.timer = setTimeout(() => {
      item.state = "queued"
      flush()
    }, delay)
  }

  const sendItem = (item: Item): boolean => {
    const id = deps.send({ case: "sendMessage", value: item.outgoing })
    if (id === null) return false
    item.state = "inflight"
    item.requestIds.push(id)
    clearTimeout(item.timer)
    item.timer = setTimeout(() => {
      // 超过 10 秒没有确认：结果未知，用同一个 client_msg_id 重发
      retryLater(item, 0, "发送超时，请重试")
    }, ACK_TIMEOUT_MS)
    return true
  }

  const setRateLimited = (until: number | undefined) => {
    rateLimitedUntil = until
    deps.onRateLimited?.(until)
  }

  // 已有定时器时沿用：到时 flush 会重新检查条件，需要的话再约下一次
  const flushAt = (at: number) => {
    if (flushTimer !== undefined) return
    flushTimer = setTimeout(() => {
      flushTimer = undefined
      flush()
    }, Math.max(0, at - now()))
  }

  // 按入队顺序发送排队中的消息，受限流暂停、发送速度和等待确认数限制
  function flush() {
    const t = now()
    if (rateLimitedUntil !== undefined) {
      if (t < rateLimitedUntil) {
        flushAt(rateLimitedUntil)
        return
      }
      setRateLimited(undefined)
    }
    sentAt = sentAt.filter((at) => t - at < SEND_WINDOW_MS)
    let inflight = 0
    for (const item of items.values()) if (item.state === "inflight") inflight++
    for (const item of items.values()) {
      if (item.state !== "queued") continue
      // 等收到确认再继续
      if (inflight >= MAX_IN_FLIGHT) return
      if (sentAt.length >= SEND_FRAMES_PER_SECOND) {
        flushAt(sentAt[0] + SEND_WINDOW_MS)
        return
      }
      if (!sendItem(item)) return
      sentAt.push(t)
      inflight++
    }
  }

  return {
    enqueue: (message, outgoing) => {
      const item: Item = { message: { ...message, status: "pending" }, outgoing, retries: 0, state: "queued", requestIds: [] }
      items.set(message.id, item)
      deps.onUpdate(item.message)
      flush()
    },

    connected: () => flush(),

    disconnected: () => {
      for (const item of items.values()) {
        if (item.state !== "inflight") continue
        clearTimeout(item.timer)
        item.retries++
        if (item.retries > MAX_AUTO_RETRIES) fail(item, "网络连接断开，发送失败")
        else item.state = "queued"
      }
    },

    handleAck: (requestId, ack) => {
      const item = findByRequest(requestId)
      if (!item) return false
      clearTimeout(item.timer)
      items.delete(item.message.id)
      deps.onAck(item.message, ack)
      flush()
      return true
    },

    handleError: (requestId, error) => {
      const item = findByRequest(requestId)
      if (!item || item.state === "failed") return item !== undefined
      const e = new ApiError(error.code, error.reason, error.message, { ...error.metadata })
      if (e.reason === "RATE_LIMITED") {
        // 回到排队状态、保持原来的位置，暂停结束后最先重发
        clearTimeout(item.timer)
        item.state = "queued"
        const until = now() + ((e.retryAfterSeconds ?? 0) * 1000 || DEFAULT_RATE_LIMIT_MS)
        if (rateLimitedUntil === undefined || until > rateLimitedUntil) {
          setRateLimited(until)
          // 之前约的定时器可能早于新的暂停结束时间，重新约
          clearTimeout(flushTimer)
          flushTimer = undefined
        }
        flush()
        return true
      }
      if (RETRYABLE.has(e.reason)) {
        retryLater(item, RETRY_STEP_MS * (item.retries + 1), errorText(e))
        return true
      }
      const text = errorText(e)
      fail(item, text)
      if (SEND_FORBIDDEN.has(e.reason)) deps.onForbidden?.(item.message, e.reason, text)
      return true
    },

    retry: (messageId) => {
      const item = items.get(messageId)
      if (!item || item.state !== "failed") return
      item.retries = 0
      item.state = "queued"
      // 移到队尾，按重试的先后发送
      items.delete(messageId)
      items.set(messageId, item)
      update(item, { status: "pending", failReason: undefined })
      flush()
    },

    hasUnconfirmed: () => [...items.values()].some((i) => i.state !== "failed"),

    clear: () => {
      for (const item of items.values()) clearTimeout(item.timer)
      items.clear()
      clearTimeout(flushTimer)
      flushTimer = undefined
      sentAt = []
      setRateLimited(undefined)
    },
  }
}
