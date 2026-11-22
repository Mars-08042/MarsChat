// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest"
import { fire, mount, type Mounted } from "@/test/dom"
import { MessageRow } from "./MessageRow"
import type { ChatMessage, Conversation } from "./types"

let mounted: Mounted | undefined

afterEach(() => {
  mounted?.unmount()
  mounted = undefined
})

const single: Conversation = { id: "10", kind: "single", peerId: "2", title: "林晚", maxSeq: 5, readSeq: 5, peerReadSeq: 4, unread: 0 }
const group: Conversation = { id: "20", kind: "group", title: "周末登山小队", memberCount: 6, maxSeq: 5, readSeq: 5, peerReadSeq: 0, unread: 0 }

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

function render(m: ChatMessage, conversation: Conversation, mine = true) {
  const onRetry = vi.fn()
  mounted = mount(
    <MessageRow
      message={m}
      conversation={conversation}
      sender={{ id: m.senderId, nickname: mine ? "阿星" : "林晚" }}
      mine={mine}
      firstInGroup
      lastInGroup
      onRetry={onRetry}
      onCancelUpload={() => {}}
    />,
  )
  const status = mounted.container.querySelector("[title]")?.getAttribute("title")
  return { container: mounted.container, onRetry, status }
}

describe("MessageRow 发送状态", () => {
  it("等待确认时显示发送中", () => {
    expect(render(message({ status: "pending", seq: undefined }), single).status).toBe("发送中")
  })

  it("单聊：对方已读位置没到这条时显示已发送，到了显示已读", () => {
    expect(render(message({ seq: 5 }), single).status).toBe("已发送")
    mounted?.unmount()
    expect(render(message({ seq: 4 }), single).status).toBe("已读")
  })

  it("群聊不显示已读", () => {
    expect(render(message({ conversationId: "20" }), { ...group, peerReadSeq: 99 }).status).toBe("已发送")
  })

  it("发送失败显示原因和重试按钮，点击重试交给调用方", () => {
    const t = render(message({ status: "failed", seq: undefined, failReason: "你已被禁言" }), single)
    expect(t.status).toBe("发送失败")
    expect(t.container.textContent).toContain("你已被禁言")
    const retry = [...t.container.querySelectorAll("button")].find((b) => b.textContent?.includes("重试"))
    if (!retry) throw new Error("retry button not rendered")
    fire(retry, new MouseEvent("click", { bubbles: true }))
    expect(t.onRetry).toHaveBeenCalledWith("1:c1")
  })

  it("对方的消息不显示发送状态", () => {
    expect(render(message({ senderId: "2" }), single, false).status).toBeUndefined()
  })
})
