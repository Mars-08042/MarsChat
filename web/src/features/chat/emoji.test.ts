import { describe, expect, it } from "vitest"
import { deleteBackward, insertAt, pushRecent } from "./emoji"

describe("insertAt", () => {
  it("在光标处插入并把光标移到插入内容之后", () => {
    expect(insertAt("你好世界", 2, 2, "😀")).toEqual({ text: "你好😀世界", caret: 4 })
  })

  it("有选中内容时替换选中部分", () => {
    expect(insertAt("hello world", 6, 11, "🔥")).toEqual({ text: "hello 🔥", caret: 8 })
  })

  it("选区方向反过来也按同一范围处理", () => {
    expect(insertAt("abcd", 3, 1, "x")).toEqual({ text: "axd", caret: 2 })
  })

  it("越界的选区收拢到文本范围内", () => {
    expect(insertAt("ab", 10, 10, "!")).toEqual({ text: "ab!", caret: 3 })
    expect(insertAt("ab", -3, -1, "!")).toEqual({ text: "!ab", caret: 1 })
  })
})

describe("deleteBackward", () => {
  it("删除光标前的一个字", () => {
    expect(deleteBackward("你好", 2, 2)).toEqual({ text: "你", caret: 1 })
  })

  it("组合表情整体删除，不留下半个", () => {
    const family = "👨‍👩‍👧"
    expect(deleteBackward(`hi${family}`, 2 + family.length, 2 + family.length)).toEqual({ text: "hi", caret: 2 })
    const thumb = "👍🏽"
    expect(deleteBackward(`a${thumb}b`, 1 + thumb.length, 1 + thumb.length)).toEqual({ text: "ab", caret: 1 })
  })

  it("有选中内容时只删除选中部分", () => {
    expect(deleteBackward("abcdef", 1, 4)).toEqual({ text: "aef", caret: 1 })
  })

  it("光标在开头时不变", () => {
    expect(deleteBackward("abc", 0, 0)).toEqual({ text: "abc", caret: 0 })
  })
})

describe("pushRecent", () => {
  it("新表情放到最前面并去重", () => {
    expect(pushRecent(["😀", "🔥", "👍"], "👍")).toEqual(["👍", "😀", "🔥"])
  })

  it("超过上限时丢掉最旧的", () => {
    expect(pushRecent(["a", "b", "c"], "d", 3)).toEqual(["d", "a", "b"])
  })
})
