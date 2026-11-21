import type { SkinTone } from "frimousse"

// 表情数据由 vite.config.ts 的 emojibaseData 插件自托管，不访问第三方 CDN（设计 3.6.4）
export const EMOJIBASE_URL = "/emojibase"
export const EMOJI_LOCALE = "zh"
// 最近使用最多记 2 行，面板按列数截取第一行显示
export const RECENT_EMOJI_LIMIT = 18

const RECENT_KEY = "marschat.emoji.recent"
const SKIN_TONE_KEY = "marschat.emoji.skin-tone"
const VIEW_KEY = "marschat.emoji.view"
// 面板上次停留的分组：平台 id，或者表示系统 emoji 的 SYSTEM_VIEW
export const SYSTEM_VIEW = "system"
const SKIN_TONES: readonly SkinTone[] = ["none", "light", "medium-light", "medium", "medium-dark", "dark"]

export interface TextEdit {
  text: string
  caret: number
}

// 在选区处插入文本，有选中内容时替换掉；光标落在插入内容之后
export function insertAt(text: string, start: number, end: number, insert: string): TextEdit {
  const from = clamp(Math.min(start, end), 0, text.length)
  const to = clamp(Math.max(start, end), 0, text.length)
  return { text: text.slice(0, from) + insert + text.slice(to), caret: from + insert.length }
}

// 手机表情面板的删除键：有选中内容时删除选中部分，否则删除光标前的一个字素。
// 按字素而不是码点删除，👨‍👩‍👧、👍🏽 这类由多个码点组成的表情一次删掉，不会留下半个；
// 光标前紧挨着一个平台表情的 :code: 时整个删掉，和选择时一次插入对应
export function deleteBackward(
  text: string,
  start: number,
  end: number,
  isEmojiCode: (code: string) => boolean = () => false,
): TextEdit {
  const from = clamp(Math.min(start, end), 0, text.length)
  const to = clamp(Math.max(start, end), 0, text.length)
  if (from !== to) return { text: text.slice(0, from) + text.slice(to), caret: from }
  if (from === 0) return { text, caret: 0 }
  if (text[from - 1] === ":") {
    const open = text.lastIndexOf(":", from - 2)
    if (open !== -1 && isEmojiCode(text.slice(open + 1, from - 1))) {
      return { text: text.slice(0, open) + text.slice(from), caret: open }
    }
  }
  const before = text.slice(0, from)
  let lastStart = 0
  for (const { index } of new Intl.Segmenter(undefined, { granularity: "grapheme" }).segment(before)) lastStart = index
  return { text: text.slice(0, lastStart) + text.slice(from), caret: lastStart }
}

// 刚用过的表情放到最前面，去重后截断
export function pushRecent(list: readonly string[], emoji: string, limit = RECENT_EMOJI_LIMIT): string[] {
  return [emoji, ...list.filter((e) => e !== emoji)].slice(0, limit)
}

// 最近使用和肤色只是便利功能，读写失败（隐私模式、数据被改坏）时按空值处理
export function readRecentEmojis(): string[] {
  try {
    const value: unknown = JSON.parse(localStorage.getItem(RECENT_KEY) ?? "[]")
    if (!Array.isArray(value)) return []
    return value.filter((e): e is string => typeof e === "string").slice(0, RECENT_EMOJI_LIMIT)
  } catch {
    return []
  }
}

export function saveRecentEmojis(list: readonly string[]) {
  try {
    localStorage.setItem(RECENT_KEY, JSON.stringify(list))
  } catch {
    // 写不进去就只在本次打开期间生效
  }
}

export function readSkinTone(): SkinTone {
  try {
    const value = localStorage.getItem(SKIN_TONE_KEY)
    return SKIN_TONES.find((t) => t === value) ?? "none"
  } catch {
    return "none"
  }
}

export function saveSkinTone(tone: SkinTone) {
  try {
    localStorage.setItem(SKIN_TONE_KEY, tone)
  } catch {
    // 同上
  }
}

// 没记录时返回 undefined，由面板决定默认分组
export function readEmojiView(): string | undefined {
  try {
    return localStorage.getItem(VIEW_KEY) ?? undefined
  } catch {
    return undefined
  }
}

export function saveEmojiView(view: string) {
  try {
    localStorage.setItem(VIEW_KEY, view)
  } catch {
    // 同上
  }
}

function clamp(n: number, min: number, max: number) {
  return Math.min(Math.max(n, min), max)
}
