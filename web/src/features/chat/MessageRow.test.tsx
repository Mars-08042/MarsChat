// @vitest-environment jsdom
import { act } from "react"
import { afterEach, describe, expect, it, vi } from "vitest"
import { fire, mount, type Mounted } from "@/test/dom"
import { MessageRow, PENDING_SPINNER_DELAY_MS } from "./MessageRow"
import type { ChatMessage, Conversation } from "./types"

let mounted: Mounted | undefined

afterEach(() => {
  mounted?.unmount()
  mounted = undefined
  vi.useRealTimers()
})

const single: Conversation = { id: "10", kind: "single", peerId: "2", title: "林晚", maxSeq: 5, readSeq: 5, peerReadSeq: 4, unread: 0 }

const message = (patch: Partial<ChatMessage> = {}): ChatMessage => ({
  id: "1:c1",
  conversationId: "10",
  senderId: "1",
  content: { kind: "text", text: "你好" },
  sentAt: Date.UTC(2026, 10, 21, 13, 0),
  status: "sent",
  seq: 5,
  ...patch,
})

function row(m: ChatMessage, { mine = true, readMark = false } = {}, onRetry = vi.fn()) {
  return (
    <MessageRow
      message={m}
      conversation={single}
      sender={{ id: m.senderId, nickname: mine ? "阿星" : "林晚" }}
      mine={mine}
      firstInGroup
      lastInGroup
      readMark={readMark}
      onRetry={onRetry}
      onCancelUpload={() => {}}
    />
  )
}

function render(m: ChatMessage, options: { mine?: boolean; readMark?: boolean } = {}) {
  const onRetry = vi.fn()
  const view = mount(row(m, options, onRetry))
  mounted = view
  const sending = () => view.container.querySelector('[role="status"]')?.textContent
  return { container: view.container, rerender: view.rerender, onRetry, sending }
}

describe("MessageRow 发送状态", () => {
  it("发送成功不显示任何标记，气泡里也没有时间", () => {
    const t = render(message())
    expect(t.sending()).toBeUndefined()
    expect(t.container.textContent).not.toMatch(/\d{2}:\d{2}|已发送|已读/)
  })

  it("发送中超过 1 秒才在气泡旁显示转圈，确认后消失", () => {
    vi.useFakeTimers()
    const t = render(message({ status: "pending", seq: undefined }))
    expect(t.sending()).toBeUndefined()
    act(() => vi.advanceTimersByTime(PENDING_SPINNER_DELAY_MS))
    expect(t.sending()).toBe("发送中")
    t.rerender(row(message()))
    expect(t.sending()).toBeUndefined()
  })

  it("确认在 1 秒内到达时不出现转圈", () => {
    vi.useFakeTimers()
    const t = render(message({ status: "pending", seq: undefined }))
    act(() => vi.advanceTimersByTime(PENDING_SPINNER_DELAY_MS / 2))
    t.rerender(row(message()))
    act(() => vi.advanceTimersByTime(PENDING_SPINNER_DELAY_MS))
    expect(t.sending()).toBeUndefined()
  })

  it("readMark 时在气泡下方显示已读", () => {
    expect(render(message(), { readMark: true }).container.textContent).toContain("已读")
    mounted?.unmount()
    expect(render(message()).container.textContent).not.toContain("已读")
  })

  it("发送失败显示原因和重试按钮，点击重试交给调用方", () => {
    const t = render(message({ status: "failed", seq: undefined, failReason: "你已被禁言" }))
    expect(t.container.textContent).toContain("你已被禁言")
    const retry = [...t.container.querySelectorAll("button")].find((b) => b.textContent?.includes("重试"))
    if (!retry) throw new Error("retry button not rendered")
    fire(retry, new MouseEvent("click", { bubbles: true }))
    expect(t.onRetry).toHaveBeenCalledWith("1:c1")
  })

  it("对方的消息不显示发送状态，头像可以点开资料卡", () => {
    const t = render(message({ senderId: "2", status: "pending" }), { mine: false })
    expect(t.sending()).toBeUndefined()
    expect(t.container.querySelector('button[aria-label="查看 林晚 的资料"]')).not.toBeNull()
  })
})
