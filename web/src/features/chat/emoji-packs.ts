import { useSyncExternalStore } from "react"

// 平台表情（微信、QQ 等）由 scripts/emoji_packs.py 生成到 web/public/emoji-packs，图片不进仓库（设计 3.6.4）。
// 消息正文里写 :code:，客户端把认识的 code 渲染成图片；没有资源包或 code 不认识时保持原文
export const EMOJI_PACKS_URL = "/emoji-packs"

export interface EmojiPackItem {
  code: string
  name: string
  url: string
  // 只有动图才有：面板和减少动态效果时用静态帧
  stillUrl?: string
}

export interface EmojiPackSet {
  id: string
  name: string
  items: EmojiPackItem[]
}

export interface EmojiPlatform {
  id: string
  name: string
  iconUrl: string
  sets: EmojiPackSet[]
}

export interface EmojiPacks {
  platforms: EmojiPlatform[]
  byCode: ReadonlyMap<string, EmojiPackItem>
}

export const NO_EMOJI_PACKS: EmojiPacks = { platforms: [], byCode: new Map() }

// 与生成脚本一致：code 不含空白和冒号；文件只允许脚本生成的命名，manifest 被改坏也不会指向别处
const CODE_MAX = 64
const CODE_RE = /^[^\s:]{1,64}$/
const FILE_RE = /^[a-z0-9-]+\/\d+(\.still)?\.webp$/
const SINGLE_CODE_RE = /^:([^\s:]{1,64}):$/

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null
}

function str(value: unknown): string | undefined {
  return typeof value === "string" ? value : undefined
}

function list(value: unknown): unknown[] {
  return Array.isArray(value) ? value : []
}

// 逐项校验，不合法的条目丢掉而不是整个失败；code 重复时保留第一个
export function parseEmojiManifest(value: unknown, base = EMOJI_PACKS_URL): EmojiPacks {
  if (!isRecord(value)) return NO_EMOJI_PACKS
  const byCode = new Map<string, EmojiPackItem>()
  const fileUrl = (file: unknown) => {
    const f = str(file)
    return f && FILE_RE.test(f) ? `${base}/${f}` : undefined
  }

  const platforms: EmojiPlatform[] = []
  for (const p of list(value.platforms)) {
    if (!isRecord(p)) continue
    const id = str(p.id)
    const name = str(p.name)
    if (!id || !name) continue
    const sets: EmojiPackSet[] = []
    for (const s of list(p.sets)) {
      if (!isRecord(s)) continue
      const items: EmojiPackItem[] = []
      for (const i of list(s.items)) {
        if (!isRecord(i)) continue
        const code = str(i.code)
        const url = fileUrl(i.file)
        if (!code || !CODE_RE.test(code) || !url || byCode.has(code)) continue
        const item: EmojiPackItem = { code, name: str(i.name) ?? code, url, stillUrl: fileUrl(i.still) }
        byCode.set(code, item)
        items.push(item)
      }
      if (items.length > 0) sets.push({ id: str(s.id) ?? `${id}-${sets.length}`, name: str(s.name) ?? name, items })
    }
    if (sets.length === 0) continue
    const first = sets[0].items[0]
    platforms.push({ id, name, iconUrl: fileUrl(p.icon) ?? first.stillUrl ?? first.url, sets })
  }
  return platforms.length > 0 ? { platforms, byCode } : NO_EMOJI_PACKS
}

export type TextPart = { kind: "text"; text: string } | { kind: "emoji"; item: EmojiPackItem }

// 把文本切成文字和平台表情。两个冒号之间不是已知 code 时，右边的冒号可能是下一个 code 的开头，
// 所以从它继续找：「时间 12:30 :qq_微笑:」「:未知::qq_微笑:」都能正确识别
export function splitEmojiCodes(text: string, byCode: ReadonlyMap<string, EmojiPackItem>): TextPart[] {
  if (byCode.size === 0 || !text.includes(":")) return [{ kind: "text", text }]
  const parts: TextPart[] = []
  let last = 0
  let open = text.indexOf(":")
  while (open !== -1) {
    const close = text.indexOf(":", open + 1)
    if (close === -1) break
    const item = close - open - 1 <= CODE_MAX ? byCode.get(text.slice(open + 1, close)) : undefined
    if (item) {
      if (open > last) parts.push({ kind: "text", text: text.slice(last, open) })
      parts.push({ kind: "emoji", item })
      last = close + 1
      open = text.indexOf(":", last)
    } else {
      open = close
    }
  }
  if (last < text.length) parts.push({ kind: "text", text: text.slice(last) })
  return parts
}

// 整段文字恰好是一个 :code: 时返回 code，用于识别最近使用里的平台表情
export function parseSingleCode(text: string): string | undefined {
  return SINGLE_CODE_RE.exec(text)?.[1]
}

// 搜索平台表情：按名称和 code 匹配，保持资源包里的顺序
export function searchEmojiPacks(packs: EmojiPacks, query: string, limit: number): EmojiPackItem[] {
  const q = query.trim().toLowerCase()
  if (!q) return []
  const result: EmojiPackItem[] = []
  for (const item of packs.byCode.values()) {
    if (item.name.toLowerCase().includes(q) || item.code.toLowerCase().includes(q)) {
      result.push(item)
      if (result.length >= limit) break
    }
  }
  return result
}

// manifest 全局只加载一次，第一个用到的组件触发
let current = NO_EMOJI_PACKS
let started = false
const listeners = new Set<() => void>()

async function load() {
  try {
    const res = await fetch(`${EMOJI_PACKS_URL}/manifest.json`)
    // 没有生成资源包是正常情况：开发服务器会回退成 index.html，按没有处理
    if (!res.ok || !res.headers.get("content-type")?.includes("json")) return
    current = parseEmojiManifest(await res.json())
    listeners.forEach((l) => l())
  } catch (err) {
    console.warn("[emoji-packs] failed to load manifest", err)
  }
}

function subscribe(listener: () => void) {
  if (!started) {
    started = true
    void load()
  }
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
  }
}

export function useEmojiPacks(): EmojiPacks {
  return useSyncExternalStore(
    subscribe,
    () => current,
    () => NO_EMOJI_PACKS,
  )
}
