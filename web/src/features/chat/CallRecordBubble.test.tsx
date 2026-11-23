// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { fire, mount, type Mounted } from "@/test/dom"
import type { CallState } from "@/realtime/call/engine"
import { callStore, initialCallState } from "@/stores/call"
import { chatStore } from "@/stores/chat"
import { CallRecordBubble } from "./CallRecordBubble"
import type { CallRecord, ConversationRef } from "./types"

const start = vi.hoisted(() => vi.fn())
vi.mock("@/realtime/client", () => ({ callActions: { start } }))

const peer = { id: "2", nickname: "林晚" }
const single: ConversationRef = { kind: "single", peerId: "2" }
let mounted: Mounted | undefined

beforeEach(() => {
  chatStore.setState({ users: { [peer.id]: peer } })
})

afterEach(() => {
  mounted?.unmount()
  mounted = undefined
  chatStore.setState({ users: {} })
  callStore.setState(initialCallState())
  start.mockReset()
})

function render(record: CallRecord, mine: boolean) {
  mounted = mount(<CallRecordBubble record={record} mine={mine} conversation={single} />)
  return mounted.container.querySelector("button")!
}

describe("CallRecordBubble", () => {
  it("按查看者是主叫还是被叫显示文案", () => {
    expect(render({ media: "video", result: "completed", durationSeconds: 83 }, true).textContent).toBe("通话时长 01:23")
    mounted?.unmount()
    expect(render({ media: "audio", result: "rejected", durationSeconds: 0 }, false).textContent).toBe("已拒绝")
  })

  it("点击按同样的类型回拨会话的对方", () => {
    const button = render({ media: "video", result: "missed", durationSeconds: 0 }, false)
    expect(button.getAttribute("aria-label")).toBe("视频通话，未接听，点击回拨")
    fire(button, new MouseEvent("click", { bubbles: true }))
    expect(start).toHaveBeenCalledWith(peer, "video")
  })

  it("自己正在通话时不能回拨", () => {
    callStore.setState({ call: {} as NonNullable<CallState["call"]> })
    const button = render({ media: "audio", result: "cancelled", durationSeconds: 0 }, true)
    expect(button.disabled).toBe(true)
    expect(button.title).toBe("你正在通话中")
    fire(button, new MouseEvent("click", { bubbles: true }))
    expect(start).not.toHaveBeenCalled()
  })
})
