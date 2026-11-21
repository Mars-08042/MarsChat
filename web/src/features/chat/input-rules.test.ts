import { describe, expect, it } from "vitest"
import { canSend, MAX_TEXT_LENGTH, shouldSend, textLength, type EnterKeyLike } from "./input-rules"

const key = (over: Partial<EnterKeyLike> = {}): EnterKeyLike => ({
  key: "Enter",
  shiftKey: false,
  isComposing: false,
  keyCode: 13,
  ...over,
})

describe("shouldSend", () => {
  it("普通 Enter 发送", () => {
    expect(shouldSend(key(), false)).toBe(true)
  })

  it("Shift+Enter 换行", () => {
    expect(shouldSend(key({ shiftKey: true }), false)).toBe(false)
  })

  it("其他按键不发送", () => {
    expect(shouldSend(key({ key: "a", keyCode: 65 }), false)).toBe(false)
  })

  it("compositionstart 到 compositionend 之间不发送", () => {
    expect(shouldSend(key(), true)).toBe(false)
  })

  it("KeyboardEvent.isComposing 为真时不发送", () => {
    expect(shouldSend(key({ isComposing: true, keyCode: 229 }), false)).toBe(false)
  })

  it("Safari：compositionend 之后到达的确认键，isComposing 为 false 但 keyCode 为 229，不发送", () => {
    expect(shouldSend(key({ keyCode: 229 }), false)).toBe(false)
  })
})

describe("textLength", () => {
  it("按码点计数，emoji 算 1 个", () => {
    expect(textLength("火星🚀")).toBe(3)
    expect("火星🚀".length).toBe(4)
  })
})

describe("canSend", () => {
  it("空白内容不能发送", () => {
    expect(canSend("")).toBe(false)
    expect(canSend("  \n\t ")).toBe(false)
  })

  it("恰好达到上限可以发送，超过不行", () => {
    expect(canSend("字".repeat(MAX_TEXT_LENGTH))).toBe(true)
    expect(canSend("字".repeat(MAX_TEXT_LENGTH + 1))).toBe(false)
  })

  it("上限按码点计算：4000 个 emoji 可以发送", () => {
    expect(canSend("🚀".repeat(MAX_TEXT_LENGTH))).toBe(true)
  })
})
