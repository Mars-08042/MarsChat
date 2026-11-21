import { memo, useMemo } from "react"
import { useReducedMotion } from "motion/react"
import { cn } from "@/lib/utils"
import { splitEmojiCodes, useEmojiPacks, type EmojiPackItem } from "./emoji-packs"

// 消息正文和会话摘要里的 :code: 渲染成平台表情图片，不认识的保持原文（设计 3.6.4）。
// 图片尺寸按字号算，跟随所在文字缩放；尺寸固定，图片加载前后不会撑动布局。
// 上下负外边距让图片不撑高行盒，会话摘要这种行高较紧的单行文字也不会因为有表情而变高
export const EmojiText = memo(function EmojiText({ text }: { text: string }) {
  const { byCode } = useEmojiPacks()
  const reduceMotion = useReducedMotion() ?? false
  const parts = useMemo(() => splitEmojiCodes(text, byCode), [text, byCode])
  return parts.map((part, i) =>
    part.kind === "text" ? (
      part.text
    ) : (
      <EmojiImage key={i} item={part.item} still={reduceMotion} className="mx-px -my-[0.2em] size-[1.5em] align-[-0.4em]" />
    ),
  )
})

export function EmojiImage({ item, still, className }: { item: EmojiPackItem; still: boolean; className?: string }) {
  return (
    <img
      src={still ? (item.stillUrl ?? item.url) : item.url}
      alt={item.name}
      title={item.name}
      draggable={false}
      loading="lazy"
      decoding="async"
      className={cn("inline-block object-contain select-none", className)}
    />
  )
}
