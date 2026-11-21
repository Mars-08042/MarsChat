import { useCallback, useEffect, useMemo, useRef, useState, type PointerEvent, type ReactNode, type RefObject } from "react"
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
  readEmojiView,
  readRecentEmojis,
  readSkinTone,
  saveEmojiView,
  saveRecentEmojis,
  saveSkinTone,
  SYSTEM_VIEW,
} from "./emoji"
import { parseSingleCode, searchEmojiPacks, useEmojiPacks, type EmojiPackItem, type EmojiPlatform } from "./emoji-packs"
import { EmojiImage } from "./EmojiText"

interface EmojiPanelProps {
  columns: number
  // 系统 emoji 传字符本身，平台表情传 :code:
  onPick: (emoji: string) => void
  // 底栏右侧的附加按钮，手机布局放删除和发送
  actions?: ReactNode
}

type Category = EmojiData["categories"][number]

interface RecentEntry {
  value: string
  item?: EmojiPackItem
}

// 表情面板（设计 3.6.4）。左侧分组导航（参考 linux.do 的 Discourse 表情面板）：平台表情在前，系统 emoji 分类在后。
// 系统 emoji 用系统字体绘制，不同设备的样式有差异；平台表情是图片，资源包缺失时只显示系统 emoji。
// 单独成文件并由输入框懒加载，frimousse 和表情数据不进首屏包
export default function EmojiPanel({ columns, onPick, actions }: EmojiPanelProps) {
  const packs = useEmojiPacks()
  const [recent, setRecent] = useState(readRecentEmojis)
  const [query, setQuery] = useState("")
  const [view, setView] = useState(readEmojiView)
  const [hovered, setHovered] = useState<EmojiPackItem | null>(null)
  const [initialSkinTone] = useState(readSkinTone)
  const [categories, setCategories] = useState<Category[]>([])
  const viewportRef = useRef<HTMLDivElement>(null)

  // 第一次打开默认停在侧栏最上面的平台；搜索时统一在系统 emoji 的列表里展示结果
  const platform = query === "" ? packs.platforms.find((p) => p.id === (view ?? packs.platforms[0]?.id)) : undefined
  const packMatches = useMemo(() => searchEmojiPacks(packs, query, columns * 2), [packs, query, columns])

  // 平台表情只在资源包里有对应 code 时显示，资源包缺失或更新后删掉的 code 不显示
  const recentEntries = useMemo(
    () =>
      recent
        .flatMap((value): RecentEntry[] => {
          const code = parseSingleCode(value)
          if (code === undefined) return [{ value }]
          const item = packs.byCode.get(code)
          return item ? [{ value, item }] : []
        })
        .slice(0, columns),
    [recent, packs, columns],
  )

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
  const pickItem = (item: EmojiPackItem) => pick(`:${item.code}:`)

  const switchView = (next: string) => {
    setView(next)
    saveEmojiView(next)
    setHovered(null)
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
            onChange={(e) => {
              setQuery(e.target.value)
              setHovered(null)
            }}
            className="min-w-0 flex-1 bg-transparent text-sm text-foreground outline-none placeholder:text-muted-foreground [&::-webkit-search-cancel-button]:hidden"
          />
        </label>
        <SkinToneButton />
      </div>

      <div className="flex min-h-0 flex-1 border-t">
        {/* 搜索时网格只剩匹配的结果，导航暂时停用 */}
        <CategoryNav
          platforms={packs.platforms}
          activePlatform={platform?.id}
          onPlatform={switchView}
          onSystem={() => switchView(SYSTEM_VIEW)}
          categories={categories}
          viewportRef={viewportRef}
          disabled={query !== ""}
        />
        <div className="flex min-w-0 flex-1 flex-col" onPointerLeave={() => setHovered(null)}>
          {query === "" && recentEntries.length > 0 && (
            <EmojiRow title="最近使用">
              {recentEntries.map((entry) =>
                entry.item ? (
                  <PackButton key={entry.value} item={entry.item} columns={columns} onPick={pickItem} onHover={setHovered} />
                ) : (
                  <button
                    key={entry.value}
                    type="button"
                    onPointerDown={keepFocus}
                    onPointerEnter={() => setHovered(null)}
                    onClick={() => pick(entry.value)}
                    aria-label={entry.value}
                    className={EMOJI_BUTTON}
                    style={{ width: `${100 / columns}%`, fontFamily: "var(--frimousse-emoji-font)" }}
                  >
                    <span className={EMOJI_GLYPH}>{entry.value}</span>
                  </button>
                ),
              )}
            </EmojiRow>
          )}
          {packMatches.length > 0 && (
            <EmojiRow title="平台表情" wrap>
              {packMatches.map((item) => (
                <PackButton key={item.code} item={item} columns={columns} onPick={pickItem} onHover={setHovered} />
              ))}
            </EmojiRow>
          )}
          {/* 系统 emoji 的列表始终挂载，平台表情盖在上面：切回系统分类时不用重新加载和测量 */}
          <div className="relative min-h-0 flex-1">
            <EmojiPicker.Viewport ref={viewportRef} className="h-full outline-none [scrollbar-width:thin]">
              <EmojiPicker.Loading className="absolute inset-0 flex items-center justify-center text-sm text-muted-foreground">
                正在加载表情…
              </EmojiPicker.Loading>
              <EmojiPicker.Empty className="absolute inset-0 flex items-center justify-center px-6 text-center text-sm text-muted-foreground">
                {({ search }) => (packMatches.length > 0 ? <>没有相关的系统表情</> : <>没有和「{search}」相关的表情</>)}
              </EmojiPicker.Empty>
              <EmojiPicker.List className="pb-2 select-none" components={{ CategoryHeader, Row, Emoji }} />
            </EmojiPicker.Viewport>
            {platform && <PackGrid key={platform.id} platform={platform} columns={columns} onPick={pickItem} onHover={setHovered} />}
          </div>
        </div>
      </div>

      <div className="flex h-11 shrink-0 items-center gap-2 border-t px-3">
        {hovered ? (
          <span className="flex min-w-0 items-center gap-2 text-sm">
            {/* 预览播放动图，网格里只显示静态帧 */}
            <EmojiImage item={hovered} still={false} className="size-6" />
            <span className="truncate text-muted-foreground">{hovered.name}</span>
          </span>
        ) : platform ? (
          <span className="truncate text-xs text-muted-foreground">{platform.name}表情，对方没有资源包时显示为文字</span>
        ) : (
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
        )}
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

const NAV_BUTTON =
  "relative flex size-8 shrink-0 items-center justify-center rounded-lg transition-colors outline-none focus-visible:ring-2 focus-visible:ring-ring/40"

function CategoryNav({
  platforms,
  activePlatform,
  onPlatform,
  onSystem,
  categories,
  viewportRef,
  disabled,
}: {
  platforms: EmojiPlatform[]
  activePlatform?: string
  onPlatform: (id: string) => void
  onSystem: () => void
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
    onSystem()
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
        // 分组比面板高，导航自己滚动；上下边缘渐隐，提示还有更多分组
        "flex w-11 shrink-0 flex-col items-center gap-0.5 overflow-y-auto border-r py-2 [scrollbar-width:none]",
        "[mask-image:linear-gradient(to_bottom,transparent,black_8px,black_calc(100%-8px),transparent)]",
        disabled && "pointer-events-none opacity-40",
      )}
    >
      {platforms.map((p) => {
        const selected = activePlatform === p.id
        return (
          <button
            key={`p-${p.id}`}
            type="button"
            onPointerDown={keepFocus}
            onClick={() => onPlatform(p.id)}
            aria-label={`${p.name}表情`}
            aria-current={selected || undefined}
            title={p.name}
            className={cn(NAV_BUTTON, !selected && "hover:bg-muted")}
          >
            {selected && <ActiveMark />}
            <img src={p.iconUrl} alt="" draggable={false} className="relative size-[22px] object-contain" />
          </button>
        )
      })}
      {platforms.length > 0 && <div role="separator" className="my-1 h-px w-6 shrink-0 bg-border" />}
      {categories.map((category) => {
        const Icon = CATEGORY_ICONS[category.index] ?? FaceSlightlySmiling
        const selected = activePlatform === undefined && active === category.label
        return (
          <button
            key={`c-${category.index}`}
            type="button"
            onPointerDown={keepFocus}
            onClick={() => jump(category.label)}
            aria-label={category.label}
            aria-current={selected || undefined}
            title={category.label}
            className={cn(NAV_BUTTON, selected ? "text-primary" : "text-muted-foreground hover:bg-muted hover:text-foreground")}
          >
            {selected && <ActiveMark />}
            <Icon className="relative size-[18px]" />
          </button>
        )
      })}
    </nav>
  )
}

function ActiveMark() {
  return <motion.span layoutId="emoji-category-active" transition={springs.snappy} className="absolute inset-0 rounded-lg bg-primary/10" />
}

// 按下时阻止默认行为，焦点留在输入框里：桌面端光标位置不丢，手机端软键盘不会弹出来（frimousse 的表情按钮也是这样做的）
function keepFocus(e: PointerEvent) {
  e.preventDefault()
}

const EMOJI_BUTTON =
  "group flex h-10 items-center justify-center rounded-lg text-[26px] leading-none outline-none data-active:bg-muted hover:bg-muted focus-visible:bg-muted"
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

function EmojiRow({ title, wrap, children }: { title: string; wrap?: boolean; children: ReactNode }) {
  return (
    // 和下面的网格一样预留滚动条槽位，两边的列才能对齐
    <div className="shrink-0 overflow-y-hidden [scrollbar-gutter:stable] [scrollbar-width:thin]">
      <div className={HEADER}>{title}</div>
      <div className={cn("flex px-2", wrap && "flex-wrap")}>{children}</div>
    </div>
  )
}

// 平台表情的网格。数量最多几百个，不做虚拟滚动；图片懒加载，只显示静态帧
function PackGrid({
  platform,
  columns,
  onPick,
  onHover,
}: {
  platform: EmojiPlatform
  columns: number
  onPick: (item: EmojiPackItem) => void
  onHover: (item: EmojiPackItem | null) => void
}) {
  return (
    <div className="absolute inset-0 overflow-y-auto bg-popover pb-2 select-none [scrollbar-gutter:stable] [scrollbar-width:thin]">
      {platform.sets.map((set) => (
        <section key={set.id} aria-label={set.name}>
          <div className={cn(HEADER, "sticky top-0 z-10")}>{platform.sets.length > 1 ? `${platform.name} · ${set.name}` : platform.name}</div>
          <div className="flex flex-wrap px-2">
            {set.items.map((item) => (
              <PackButton key={item.code} item={item} columns={columns} onPick={onPick} onHover={onHover} />
            ))}
          </div>
        </section>
      ))}
    </div>
  )
}

function PackButton({
  item,
  columns,
  onPick,
  onHover,
}: {
  item: EmojiPackItem
  columns: number
  onPick: (item: EmojiPackItem) => void
  onHover: (item: EmojiPackItem | null) => void
}) {
  return (
    <button
      type="button"
      onPointerDown={keepFocus}
      onPointerEnter={() => onHover(item)}
      onFocus={() => onHover(item)}
      onClick={() => onPick(item)}
      aria-label={item.name}
      className={EMOJI_BUTTON}
      style={{ width: `${100 / columns}%` }}
    >
      <EmojiImage item={item} still className={cn(EMOJI_GLYPH, "size-7")} />
    </button>
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
