// @vitest-environment jsdom
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest"
import { TooltipProvider } from "@/components/ui/tooltip"
import { fire, mount, setValue, stubMatchMedia, type Mounted } from "@/test/dom"
import { Composer } from "./Composer"
import { MAX_TEXT_LENGTH } from "./input-rules"

let mounted: Mounted | undefined

beforeAll(() => stubMatchMedia(true))
afterEach(() => {
  mounted?.unmount()
  mounted = undefined
})

function setup() {
  const onSend = vi.fn()
  mounted = mount(
    <TooltipProvider>
      <Composer onSend={onSend} onFiles={() => {}} />
    </TooltipProvider>,
  )
  const textarea = mounted.container.querySelector("textarea")
  if (!textarea) throw new Error("textarea not rendered")
  return { onSend, textarea, container: mounted.container }
}

const enter = (init: KeyboardEventInit = {}) => new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true, ...init })
const composition = (type: "compositionstart" | "compositionend") => new CompositionEvent(type, { bubbles: true })

describe("Composer", () => {
  it("Enter 发送去掉首尾空白的内容并清空输入框", () => {
    const t = setup()
    setValue(t.textarea, "  你好  ")
    fire(t.textarea, enter())
    expect(t.onSend).toHaveBeenCalledWith("你好")
    expect(t.textarea.value).toBe("")
  })

  it("输入法选词期间按 Enter 是确认候选词，不发送", () => {
    const t = setup()
    fire(t.textarea, composition("compositionstart"))
    setValue(t.textarea, "ni")
    fire(t.textarea, enter())
    expect(t.onSend).not.toHaveBeenCalled()

    fire(t.textarea, composition("compositionend"))
    setValue(t.textarea, "你")
    fire(t.textarea, enter())
    expect(t.onSend).toHaveBeenCalledWith("你")
  })

  it("keydown 带 isComposing 或 keyCode 229（Safari 在 compositionend 之后才派发）时不发送", () => {
    const t = setup()
    setValue(t.textarea, "你好")
    fire(t.textarea, enter({ isComposing: true }))
    fire(t.textarea, enter({ keyCode: 229 }))
    expect(t.onSend).not.toHaveBeenCalled()
    expect(t.textarea.value).toBe("你好")
  })

  it("Shift+Enter 换行，不发送", () => {
    const t = setup()
    setValue(t.textarea, "第一行")
    fire(t.textarea, enter({ shiftKey: true }))
    expect(t.onSend).not.toHaveBeenCalled()
  })

  it("只有空白时不发送", () => {
    const t = setup()
    setValue(t.textarea, "   \n ")
    fire(t.textarea, enter())
    expect(t.onSend).not.toHaveBeenCalled()
  })

  it("超出字数上限时不发送，并提示超出的字数（按码点计数）", () => {
    const t = setup()
    // 每个 emoji 是一个码点、两个 UTF-16 单元
    setValue(t.textarea, "😀".repeat(MAX_TEXT_LENGTH + 3))
    fire(t.textarea, enter())
    expect(t.onSend).not.toHaveBeenCalled()
    expect(t.container.textContent).toContain("超出 3 字")
  })
})
