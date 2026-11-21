import { describe, expect, it } from "vitest"
import tokensCss from "./tokens.css?raw"

// 按 WCAG 2.x 的相对亮度公式核对设计变量的对比度（设计 3.4.2）。
// 正文 4.5:1；焦点环等非文字元素 3:1。

type Theme = Record<string, string>

function parseTheme(selector: string): Theme {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")
  const block = tokensCss.match(new RegExp(`${escaped}\\s*\\{([^}]*)\\}`))
  if (!block) throw new Error(`selector ${selector} not found in tokens.css`)
  const vars: Theme = {}
  for (const [, name, hex] of block[1].matchAll(/--([\w-]+):\s*(#[0-9a-fA-F]{6})\b/g)) {
    vars[name] = hex
  }
  return vars
}

function luminance(hex: string): number {
  const channels = [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16) / 255)
  const [r, g, b] = channels.map((c) => (c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4))
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}

function contrast(a: string, b: string): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x)
  return (hi + 0.05) / (lo + 0.05)
}

// [前景变量, 背景变量, 最低对比度]
const PAIRS: Array<[string, string, number]> = [
  ["foreground", "background", 4.5],
  ["card-foreground", "card", 4.5],
  ["popover-foreground", "popover", 4.5],
  ["primary-foreground", "primary", 4.5],
  ["secondary-foreground", "secondary", 4.5],
  ["muted-foreground", "background", 4.5],
  ["muted-foreground", "card", 4.5],
  ["muted-foreground", "muted", 4.5],
  ["accent-foreground", "accent", 4.5],
  ["destructive", "background", 4.5],
  ["destructive", "card", 4.5],
  ["success", "background", 4.5],
  ["success", "card", 4.5],
  ["brand-text", "background", 4.5],
  ["brand-text", "card", 4.5],
  ["brand-text", "accent", 4.5],
  ["primary-foreground", "brand-strong", 4.5], // 我方气泡
  ["foreground", "bubble-peer", 4.5], // 对方气泡
  ["gold-foreground", "gold", 4.5], // 未读徽章
  ["ring", "background", 3],
  ["ring", "card", 3],
]

describe.each([
  ["light", ":root"],
  ["dark", ".dark"],
])("%s 主题对比度", (_name, selector) => {
  const theme = parseTheme(selector)

  it.each(PAIRS)("%s / %s ≥ %d", (fg, bg, min) => {
    expect(theme[fg], `--${fg} 未定义`).toBeDefined()
    expect(theme[bg], `--${bg} 未定义`).toBeDefined()
    expect(contrast(theme[fg], theme[bg])).toBeGreaterThanOrEqual(min)
  })
})

describe("已知结论", () => {
  // 设计中的关键判断：白字不能直接放在亮火星红上，这条断言防止以后有人把 primary 改回 #E2452C
  it("白字 / #E2452C 不满足正文对比度", () => {
    expect(contrast("#FFFFFF", "#E2452C")).toBeLessThan(4.5)
  })
})
