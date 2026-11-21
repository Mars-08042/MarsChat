import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar"
import { cn } from "@/lib/utils"

// 没有头像时用首字加渐变底色。渐变都取暖色，且足够深，保证白字可读（设计 3.4.1：火星红之外只用暖色）
const GRADIENTS: ReadonlyArray<readonly [string, string]> = [
  ["#C2361F", "#E2452C"],
  ["#A8321C", "#D9603B"],
  ["#9A3412", "#C2410C"],
  ["#8C2F1B", "#B4532A"],
  ["#6E4A3A", "#9C6B4E"],
  ["#3D2B26", "#6E5248"],
  ["#7A2E3A", "#B24A4A"],
]

function pickGradient(seed: string): readonly [string, string] {
  let hash = 0
  for (const ch of seed) hash = (hash * 31 + ch.charCodeAt(0)) | 0
  return GRADIENTS[Math.abs(hash) % GRADIENTS.length]
}

function initial(name: string): string {
  const first = [...name.trim()][0] ?? "?"
  return first.toUpperCase()
}

// [容器尺寸, 首字字号]；字号要写在 AvatarFallback 上，它自带的 text-sm 会覆盖继承的字号
const SIZES = {
  sm: ["size-7", "text-xs"],
  md: ["size-10", "text-sm"],
  lg: ["size-12", "text-base"],
  // 来电卡片
  xl: ["size-14", "text-lg"],
  // 通话全屏界面
  hero: ["size-32", "text-4xl"],
} as const

interface UserAvatarProps {
  // 用户 ID 或群 ID，决定渐变颜色，改名后颜色不变
  seed: string
  name: string
  src?: string
  // 群用圆角方形，和个人头像区分
  shape?: "circle" | "square"
  size?: keyof typeof SIZES
  className?: string
}

export function UserAvatar({ seed, name, src, shape = "circle", size = "md", className }: UserAvatarProps) {
  const [from, to] = pickGradient(seed)
  const [box, text] = SIZES[size]
  const radius = shape === "square" ? "rounded-[30%] after:rounded-[30%]" : "rounded-full"

  return (
    <Avatar className={cn(box, radius, className)}>
      {src && <AvatarImage src={src} alt={name} className={radius} />}
      <AvatarFallback
        className={cn("font-medium text-white", text, radius)}
        style={{ backgroundImage: `linear-gradient(135deg, ${from}, ${to})` }}
      >
        {initial(name)}
      </AvatarFallback>
    </Avatar>
  )
}
