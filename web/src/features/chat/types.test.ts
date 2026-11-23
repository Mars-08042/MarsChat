import { describe, expect, it } from "vitest"
import { readMarkId, type ChatMessage, type Conversation } from "./types"

const ME = "1"
const PEER = "2"

const single: Conversation = { id: "10", kind: "single", peerId: PEER, title: "林晚", maxSeq: 3, readSeq: 3, peerReadSeq: 0, unread: 0 }

let n = 0
const msg = (senderId: string, patch: Partial<ChatMessage> = {}): ChatMessage => {
  n++
  return { id: `m${n}`, conversationId: "10", senderId, content: { kind: "text", text: "x" }, sentAt: n, status: "sent", seq: n, ...patch }
}

describe("readMarkId", () => {
  it("对方读到我方最新一条时标在这条上", () => {
    const a = msg(ME, { seq: 1 })
    const b = msg(ME, { seq: 2 })
    expect(readMarkId([a, b], { ...single, peerReadSeq: 2 }, ME)).toBe(b.id)
  })

  it("最新一条还没读时不标，即使更早的消息已读", () => {
    const a = msg(ME, { seq: 1 })
    const b = msg(ME, { seq: 2 })
    expect(readMarkId([a, b], { ...single, peerReadSeq: 1 }, ME)).toBeUndefined()
  })

  it("发送中和失败的消息跳过，已读留在上一条已确认的消息上", () => {
    const a = msg(ME, { seq: 1 })
    const pending = msg(ME, { status: "pending", seq: undefined })
    const failed = msg(ME, { status: "failed", seq: undefined })
    expect(readMarkId([a, pending, failed], { ...single, peerReadSeq: 1 }, ME)).toBe(a.id)
  })

  it("对方在我之后发过言时不标", () => {
    const a = msg(ME, { seq: 1 })
    const b = msg(PEER, { seq: 2 })
    expect(readMarkId([a, b], { ...single, peerReadSeq: 2 }, ME)).toBeUndefined()
  })

  it("群聊不标已读", () => {
    const a = msg(ME, { seq: 1 })
    const group: Conversation = { ...single, kind: "group", peerId: undefined, peerReadSeq: 99 }
    expect(readMarkId([a], group, ME)).toBeUndefined()
  })

  it("没有消息时不标", () => {
    expect(readMarkId([], single, ME)).toBeUndefined()
  })
})
