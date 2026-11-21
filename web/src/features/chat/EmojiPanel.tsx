import { useCallback, useEffect, useRef, useState, type PointerEvent, type ReactNode, type RefObject } from "react"
import { motion } from "motion/react"
import {
  defaultEmojiDataResolver,
  EmojiPicker,
  type EmojiData,
  type EmojiDataResolver,
  type EmojiPickerListCategoryHeaderProps,
  type EmojiPickerListEmojiProps,
  type EmojiPickerListRowProps,
} from "frimousse"
import {
  Apple,
  FaceSlightlySmiling,
  Flag,
  Hand,
  Lightbulb,
  PawPrint,
  Plane,
  Search,
  Shapes,
  Volleyball,
  type LucideIcon,
} from "lucide-react"
import { springs } from "@/lib/motion"
import { cn } from "@/lib/utils"
import {
  EMOJI_LOCALE,
  EMOJIBASE_URL,
  pushRecent,
  readRecentEmojis,
  readSkinTone,
  saveRecentEmojis,
  saveSkinTone,
} from "./emoji"

interface EmojiPanelProps {
  columns: number
  onPick: (emoji: string) => void
  // 底栏右侧的附加按钮，手机布局放删除和发送
  actions?: ReactNode
}

type Category = EmojiData["categories"][number]

// 表情面板（设计 3.6.4）。只有系统 emoji，用系统字体绘制，不同设备的样式会有差异。
// 左侧是分组导航（参考 linux.do 的 Discourse 表情面板），右侧是最近使用和表情网格。
// 单独成文件并由输入框懒加载，frimousse 和表情数据不进首屏包
export default function EmojiPanel({ columns, onPick, actions }: EmojiPanelProps) {
  const [recent, setRecent] = useState(readRecentEmojis)
  const [query, setQuery] = useState("")
  const [initialSkinTone] = useState(readSkinTone)
  const [categories, setCategories] = useState<Category[]>([])
  const viewportRef = useRef<HTMLDivElement>(null)

  // frimousse 不直接暴露分类列表，包一层默认解析器把分类记下来。
  // 仍然委托给默认解析器，按系统支持的 emoji 版本和国旗过滤的逻辑照常生效
  const resolveEmojiData = useCallback<EmojiDataResolver>(async (locale, options) => {
    const data = await defaultEmojiDataResolver(locale, options)
    setCategories(data.categories)
    return data
  }, [])

  const pick = (emoji: string) => {
    const next = pushRecent(recent, emoji)
    setRecent(next)
    saveRecentEmojis(next)
    onPick(emoji)
  }

  return (
    <EmojiPicker.Root
      locale={EMOJI_LOCALE}
      emojibaseUrl={EMOJIBASE_URL}
      resolveEmojiData={resolveEmojiData}
      columns={columns}
      skinTone={initialSkinTone}
      onEmojiSelect={(e) => pick(e.emoji)}
      className="flex h-full w-full flex-col"
    >
      <div className="flex items-center gap-1.5 px-3 pt-3 pb-2">
        <label className="flex h-9 min-w-0 flex-1 items-center gap-2 rounded-lg bg-muted px-2.5 text-muted-foreground transition-shadow focus-within:ring-2 focus-within:ring-ring/30">
          <Search className="size-4 shrink-0" />
          <EmojiPicker.Search
            placeholder="搜索表情，如：笑、猫、火"
            aria-label="搜索表情"
            onChange={(e) => setQuery(e.target.value)}
            className="min-w-0 flex-1 bg-transparent text-sm text-foreground outline-none placeholder:text-muted-foreground [&::-webkit-search-cancel-button]:hidden"
          />
        </label>
        <SkinToneButton />
      </div>

      <div className="flex min-h-0 flex-1 border-t">
        {/* 搜索时网格只剩匹配的分类，导航暂时停用 */}
        <CategoryNav categories={categories} viewportRef={viewportRef} disabled={query !== ""} />
        <div className="flex min-w-0 flex-1 flex-col">
          {query === "" && recent.length > 0 && <RecentRow emojis={recent.slice(0, columns)} columns={columns} onPick={pick} />}
          <EmojiPicker.Viewport ref={viewportRef} className="relative min-h-0 flex-1 outline-none [scrollbar-width:thin]">
            <EmojiPicker.Loading className="absolute inset-0 flex items-center justify-center text-sm text-muted-foreground">
              正在加载表情…
            </EmojiPicker.Loading>
            <EmojiPicker.Empty className="absolute inset-0 flex items-center justify-center px-6 text-center text-sm text-muted-foreground">
              {({ search }) => <>没有和「{search}」相关的表情</>}
            </EmojiPicker.Empty>
            <EmojiPicker.List className="pb-2 select-none" components={{ CategoryHeader, Row, Emoji }} />
          </EmojiPicker.Viewport>
        </div>
      </div>

      <div className="flex h-11 shrink-0 items-center gap-2 border-t px-3">
        <EmojiPicker.ActiveEmoji>
          {({ emoji }) =>
            emoji ? (
              <span className="flex min-w-0 items-center gap-2 text-sm">
                <span className="text-xl leading-none" style={{ fontFamily: "var(--frimousse-emoji-font)" }}>
                  {emoji.emoji}
                </span>
                <span className="truncate text-muted-foreground">{emoji.label}</span>
              </span>
            ) : (
              <span className="truncate text-xs text-muted-foreground">系统表情，不同设备上的样式略有差异</span>
            )
          }
        </EmojiPicker.ActiveEmoji>
        {actions && <div className="ml-auto flex shrink-0 items-center gap-1.5">{actions}</div>}
      </div>
    </EmojiPicker.Root>
  )
}

// 按 Emojibase 的分组序号取图标，序号来自 Unicode CLDR 分组，跨版本稳定；分组名称用数据里的本地化文字
const CATEGORY_ICONS: Record<number, LucideIcon> = {
  0: FaceSlightlySmiling,
  1: Hand,
  3: PawPrint,
  4: Apple,
  5: Plane,
  6: Volleyball,
  7: Lightbulb,
  8: Shapes,
  9: Flag,
}

// 每个分类在网格里的起始位置。frimousse 的列表是虚拟滚动的，但每个分类的容器（只含吸顶标题）始终渲染，
// 位置按行高算好，可以直接用来跳转和判断当前分类
function categoryOffsets(viewport: HTMLElement) {
  const base = viewport.getBoundingClientRect().top - viewport.scrollTop
  return Array.from(viewport.querySelectorAll<HTMLElement>("[frimousse-category]"), (el) => ({
    label: el.textContent ?? "",
    top: el.getBoundingClientRect().top - base,
  }))
}

function CategoryNav({
  categories,
  viewportRef,
  disabled,
}: {
  categories: Category[]
  viewportRef: RefObject<HTMLDivElement | null>
  disabled: boolean
}) {
  const [active, setActive] = useState<string | undefined>()

  // 滚动时高亮当前所在的分类
  useEffect(() => {
    const viewport = viewportRef.current
    if (!viewport || categories.length === 0) return
    let frame = 0
    const update = () => {
      frame = 0
      const y = viewport.scrollTop + 4
      const current = categoryOffsets(viewport).findLast((c) => c.top <= y)
      // 列表还没渲染出来时停在顶部，即第一个分类
      setActive(current?.label ?? categories[0]?.label)
    }
    const onScroll = () => {
      if (!frame) frame = requestAnimationFrame(update)
    }
    update()
    viewport.addEventListener("scroll", onScroll, { passive: true })
    return () => {
      viewport.removeEventListener("scroll", onScroll)
      cancelAnimationFrame(frame)
    }
  }, [categories, viewportRef])

  const jump = (label: string) => {
    const viewport = viewportRef.current
    const target = viewport && categoryOffsets(viewport).find((c) => c.label === label)
    if (!viewport || !target) return
    viewport.scrollTo({ top: target.top, behavior: "instant" })
    setActive(label)
  }

  return (
    <nav
      aria-label="表情分组"
      aria-disabled={disabled || undefined}
      className={cn(
        "flex w-11 shrink-0 flex-col items-center gap-0.5 overflow-y-auto border-r py-1.5 [scrollbar-width:none]",
        disabled && "pointer-events-none opacity-40",
      )}
    >
      {categories.map((category) => {
        const Icon = CATEGORY_ICONS[category.index] ?? FaceSlightlySmiling
        const selected = active === category.label
        return (
          <button
            key={category.index}
            type="button"
            onPointerDown={keepFocus}
            onClick={() => jump(category.label)}
            aria-label={category.label}
            aria-current={selected || undefined}
            title={category.label}
            className={cn(
              "relative flex size-8 shrink-0 items-center justify-center rounded-lg transition-colors outline-none focus-visible:ring-2 focus-visible:ring-ring/40",
              selected ? "text-primary" : "text-muted-foreground hover:bg-muted hover:text-foreground",
            )}
          >
            {selected && (
              <motion.span
                layoutId="emoji-category-active"
                transition={springs.snappy}
                className="absolute inset-0 rounded-lg bg-primary/10"
              />
            )}
            <Icon className="relative size-[18px]" />
          </button>
        )
      })}
    </nav>
  )
}

// 按下时阻止默认行为，焦点留在输入框里：桌面端光标位置不丢，手机端软键盘不会弹出来（frimousse 的表情按钮也是这样做的）
function keepFocus(e: PointerEvent) {
  e.preventDefault()
}

const EMOJI_BUTTON =
  "group flex h-10 items-center justify-center rounded-lg text-[26px] leading-none outline-none data-active:bg-muted hover:bg-muted"
const EMOJI_GLYPH = "transition-transform duration-150 group-hover:scale-115 group-data-active:scale-115 motion-reduce:transition-none"
const HEADER = "bg-popover px-3 pt-3 pb-1.5 text-xs font-medium text-muted-foreground"

function CategoryHeader({ category, ...props }: EmojiPickerListCategoryHeaderProps) {
  return (
    <div {...props} className={HEADER}>
      {category.label}
    </div>
  )
}

function Row({ children, ...props }: EmojiPickerListRowProps) {
  return (
    <div {...props} className="scroll-my-1.5 px-2">
      {children}
    </div>
  )
}

function Emoji({ emoji, ...props }: EmojiPickerListEmojiProps) {
  return (
    // 每行固定列数，按钮宽度按列数均分；frimousse 要求所有按钮尺寸一致
    <button {...props} className={cn(EMOJI_BUTTON, "w-[calc(100%/var(--frimousse-list-columns))]")}>
      <span className={EMOJI_GLYPH}>{emoji.emoji}</span>
    </button>
  )
}

function RecentRow({ emojis, columns, onPick }: { emojis: string[]; columns: number; onPick: (emoji: string) => void }) {
  return (
    // 和下面的网格一样预留滚动条槽位，两边的列才能对齐
    <div className="shrink-0 overflow-y-hidden [scrollbar-gutter:stable] [scrollbar-width:thin]">
      <div className={HEADER}>最近使用</div>
      <div className="flex px-2">
        {emojis.map((emoji) => (
          <button
            key={emoji}
            type="button"
            onPointerDown={keepFocus}
            onClick={() => onPick(emoji)}
            aria-label={emoji}
            className={EMOJI_BUTTON}
            style={{ width: `${100 / columns}%`, fontFamily: "var(--frimousse-emoji-font)" }}
          >
            <span className={EMOJI_GLYPH}>{emoji}</span>
          </button>
        ))}
      </div>
    </div>
  )
}

// 点击依次切换 6 种肤色，选择记在本地
function SkinToneButton() {
  return (
    <EmojiPicker.SkinTone>
      {({ skinTone, setSkinTone, skinToneVariations }) => {
        const index = Math.max(
          0,
          skinToneVariations.findIndex((v) => v.skinTone === skinTone),
        )
        const next = skinToneVariations[(index + 1) % skinToneVariations.length]
        return (
          <button
            type="button"
            onPointerDown={keepFocus}
            onClick={() => {
              setSkinTone(next.skinTone)
              saveSkinTone(next.skinTone)
            }}
            aria-label="切换肤色"
            title="切换肤色"
            className="flex size-9 shrink-0 items-center justify-center rounded-lg text-xl leading-none transition-colors outline-none hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring/40"
            style={{ fontFamily: "var(--frimousse-emoji-font)" }}
          >
            {skinToneVariations[index].emoji}
          </button>
        )
      }}
    </EmojiPicker.SkinTone>
  )
}
