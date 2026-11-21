import { describe, expect, it } from "vitest"
import { NO_EMOJI_PACKS, parseEmojiManifest, parseSingleCode, searchEmojiPacks, splitEmojiCodes, type TextPart } from "./emoji-packs"

const manifest = {
  generator: "marschat-emoji-packs",
  version: 1,
  platforms: [
    {
      id: "wechat",
      name: "微信",
      icon: "wechat/001.webp",
      sets: [
        {
          id: "wechat",
          name: "微信",
          items: [
            { code: "wechat_微笑", name: "微笑", file: "wechat/001.webp" },
            { code: "wechat_666", name: "666", file: "wechat/002.webp" },
          ],
        },
      ],
    },
    {
      id: "qq",
      name: "QQ",
      icon: "qq/001.still.webp",
      sets: [
        { id: "qq", name: "小黄脸", items: [{ code: "qq_微笑", name: "微笑", file: "qq/001.webp", still: "qq/001.still.webp" }] },
        { id: "qq-classic", name: "经典", items: [{ code: "qqclassic_smile", name: "smile", file: "qq-classic/001.webp" }] },
      ],
    },
  ],
}

const packs = parseEmojiManifest(manifest, "/p")

// 把切分结果转成便于断言的形式：文字原样，表情写成 <code>
function render(parts: TextPart[]) {
  return parts.map((p) => (p.kind === "text" ? p.text : `<${p.item.code}>`)).join("|")
}

describe("parseEmojiManifest", () => {
  it("按平台和子集保留顺序，路径拼上前缀", () => {
    expect(packs.platforms.map((p) => p.id)).toEqual(["wechat", "qq"])
    expect(packs.platforms[1].sets.map((s) => s.name)).toEqual(["小黄脸", "经典"])
    expect(packs.byCode.get("qq_微笑")).toEqual({ code: "qq_微笑", name: "微笑", url: "/p/qq/001.webp", stillUrl: "/p/qq/001.still.webp" })
    expect(packs.platforms[1].iconUrl).toBe("/p/qq/001.still.webp")
  })

  it("丢掉不合法的条目而不是整体失败", () => {
    const result = parseEmojiManifest({
      platforms: [
        {
          id: "x",
          name: "X",
          sets: [
            {
              id: "x",
              items: [
                { code: "x_ok", name: "ok", file: "x/001.webp" },
                { code: "带 空格", file: "x/002.webp" },
                { code: "x:colon", file: "x/003.webp" },
                { code: "x_path", file: "../secret.webp" },
                { code: "x_abs", file: "https://evil.example/a.webp" },
                { code: "x_ok", name: "重复", file: "x/004.webp" },
                "not an object",
              ],
            },
          ],
        },
        { id: "empty", name: "空", sets: [{ id: "e", items: [] }] },
      ],
    })
    expect([...result.byCode.keys()]).toEqual(["x_ok"])
    expect(result.byCode.get("x_ok")?.name).toBe("ok")
    // 没有 icon 时用第一个表情
    expect(result.platforms.map((p) => [p.id, p.iconUrl])).toEqual([["x", "/emoji-packs/x/001.webp"]])
  })

  it("格式不对时返回空资源包", () => {
    expect(parseEmojiManifest(null)).toBe(NO_EMOJI_PACKS)
    expect(parseEmojiManifest("<!doctype html>")).toBe(NO_EMOJI_PACKS)
    expect(parseEmojiManifest({ platforms: [] })).toBe(NO_EMOJI_PACKS)
  })
})

describe("splitEmojiCodes", () => {
  const split = (text: string) => render(splitEmojiCodes(text, packs.byCode))

  it("识别文字中间和相邻的 code", () => {
    expect(split("好的:wechat_微笑:")).toBe("好的|<wechat_微笑>")
    expect(split(":qq_微笑::wechat_666:")).toBe("<qq_微笑>|<wechat_666>")
    expect(split("a :qq_微笑: b")).toBe("a |<qq_微笑>| b")
  })

  it("不认识的 code 保持原文", () => {
    expect(split(":不存在: 你好")).toBe(":不存在: 你好")
    expect(split("12:30")).toBe("12:30")
  })

  it("不认识的部分后面紧跟已知 code 时仍能识别", () => {
    expect(split("时间 12:30:qq_微笑:")).toBe("时间 12:30|<qq_微笑>")
    expect(split(":未知::qq_微笑:")).toBe(":未知:|<qq_微笑>")
  })

  it("code 中间有空白时不识别", () => {
    expect(split(":qq_ 微笑:")).toBe(":qq_ 微笑:")
  })

  it("没有资源包时整段作为文字", () => {
    expect(splitEmojiCodes(":qq_微笑:", NO_EMOJI_PACKS.byCode)).toEqual([{ kind: "text", text: ":qq_微笑:" }])
  })
})

describe("parseSingleCode", () => {
  it("只在整段恰好是一个 code 时返回", () => {
    expect(parseSingleCode(":qq_微笑:")).toBe("qq_微笑")
    expect(parseSingleCode("😀")).toBeUndefined()
    expect(parseSingleCode(":a::b:")).toBeUndefined()
  })
})

describe("searchEmojiPacks", () => {
  it("按名称和 code 匹配并限制数量", () => {
    expect(searchEmojiPacks(packs, "微笑", 10).map((i) => i.code)).toEqual(["wechat_微笑", "qq_微笑"])
    expect(searchEmojiPacks(packs, "SMILE", 10).map((i) => i.code)).toEqual(["qqclassic_smile"])
    expect(searchEmojiPacks(packs, "微笑", 1)).toHaveLength(1)
    expect(searchEmojiPacks(packs, "  ", 10)).toEqual([])
  })
})
