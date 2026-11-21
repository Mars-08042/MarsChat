import { useMemo, useState } from "react"
import { motion } from "motion/react"
import { Link } from "react-router"
import { CircleExclamationPoint, Clock3, Search, SquarePen } from "lucide-react"
import { UserAvatar } from "@/components/common/UserAvatar"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { springs } from "@/lib/motion"
import { formatListTime } from "@/lib/time"
import { useNow } from "@/lib/use-countdown"
import { cn } from "@/lib/utils"
import { EmojiText } from "./EmojiText"
import { useChatStore } from "./fake-store"
import { contentPreview, type ChatMessage, type Conversation } from "./types"

interface Row {
  conversation: Conversation
  last?: ChatMessage
}

export function ConversationList({ activeId, className }: { activeId?: string; className?: string }) {
  const conversations = useChatStore((s) => s.conversations)
  const messages = useChatStore((s) => s.messages)
  const [query, setQuery] = useState("")
  const now = useNow()

  // 按最后一条消息的时间倒序；搜索只过滤已加载的会话名称（后端没有搜索接口）
  const rows = useMemo<Row[]>(() => {
    const keyword = query.trim().toLowerCase()
    return Object.values(conversations)
      .filter((c) => keyword === "" || c.title.toLowerCase().includes(keyword))
      .map((c) => ({ conversation: c, last: messages[c.id]?.at(-1) }))
      .sort((a, b) => (b.last?.sentAt ?? 0) - (a.last?.sentAt ?? 0))
  }, [conversations, messages, query])

  return (
    <aside className={cn("flex w-full shrink-0 flex-col border-r bg-card md:w-80 xl:w-[22rem]", className)}>
      <div className="flex h-16 shrink-0 items-center justify-between px-5">
        <h1 className="font-display text-xl font-semibold tracking-tight">消息</h1>
        <Button variant="ghost" size="icon" aria-label="新建会话">
          <SquarePen />
        </Button>
      </div>
      <div className="px-3 pb-2">
        <div className="relative">
          <Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="搜索会话"
            aria-label="搜索会话"
            className="h-9 rounded-lg border-transparent bg-muted pl-9 dark:bg-muted"
          />
        </div>
      </div>
      {/* layoutScroll 让 layout 动画在列表滚动后仍能正确测量位置 */}
      <motion.ul layoutScroll className="min-h-0 flex-1 space-y-0.5 overflow-y-auto px-2 pb-3 [scrollbar-width:thin]">
        {rows.map((row) => (
          <ConversationItem key={row.conversation.id} row={row} active={row.conversation.id === activeId} now={now} />
        ))}
        {rows.length === 0 && <li className="px-3 py-10 text-center text-sm text-muted-foreground">没有找到相关会话</li>}
      </motion.ul>
    </aside>
  )
}

function ConversationItem({ row, active, now }: { row: Row; active: boolean; now: number }) {
  const { conversation: c, last } = row
  const me = useChatStore((s) => s.me)
  const users = useChatStore((s) => s.users)

  return (
    // 收到新消息时会话平滑移到顶部（设计 3.6.2）
    <motion.li layout="position" transition={springs.smooth}>
      <Link
        to={`/chat/${c.id}`}
        aria-current={active ? "page" : undefined}
        className={cn(
          "relative flex items-center gap-3 rounded-xl px-3 py-2.5 transition-colors outline-none focus-visible:ring-2 focus-visible:ring-ring",
          active ? "bg-accent" : "hover:bg-muted/70",
        )}
      >
        {active && (
          <motion.span
            layoutId="conversation-active"
            transition={springs.smooth}
            className="absolute top-3 bottom-3 left-0 w-1 rounded-r-full bg-brand"
          />
        )}
        <UserAvatar seed={c.peerId ?? c.id} name={c.title} shape={c.kind === "group" ? "square" : "circle"} />
        <div className="min-w-0 flex-1">
          <div className="flex items-baseline justify-between gap-2">
            <span className="truncate font-medium">{c.title}</span>
            {last && (
              <time className="shrink-0 font-mono text-[11px] text-muted-foreground">{formatListTime(last.sentAt, now)}</time>
            )}
          </div>
          <div className="mt-0.5 flex items-center justify-between gap-2">
            <Preview conversation={c} last={last} meId={me.id} senderName={last ? users[last.senderId]?.nickname : undefined} />
            {c.unread > 0 && <UnreadBadge count={c.unread} />}
          </div>
        </div>
      </Link>
    </motion.li>
  )
}

function Preview({
  conversation,
  last,
  meId,
  senderName,
}: {
  conversation: Conversation
  last?: ChatMessage
  meId: string
  senderName?: string
}) {
  if (!last) return <span className="text-sm text-muted-foreground">暂无消息</span>
  const mine = last.senderId === meId
  const prefix = mine ? "你：" : conversation.kind === "group" && senderName ? `${senderName}：` : ""

  return (
    <span className="flex min-w-0 items-center gap-1 text-sm text-muted-foreground">
      {mine && last.status === "failed" && <CircleExclamationPoint className="size-3.5 shrink-0 text-destructive" aria-label="发送失败" />}
      {mine && (last.status === "pending" || last.status === "uploading") && <Clock3 className="size-3.5 shrink-0" aria-label="发送中" />}
      <span className="truncate">
        {prefix}
        <EmojiText text={contentPreview(last.content)} />
      </span>
    </span>
  )
}

// 沙金色未读徽章，数字变化时用 snappy 弹一下；超过 99 显示 99+
function UnreadBadge({ count }: { count: number }) {
  return (
    <motion.span
      key={count}
      initial={{ scale: 0.6 }}
      animate={{ scale: 1 }}
      transition={springs.snappy}
      aria-label={`${count} 条未读`}
      className="h-5 min-w-5 shrink-0 rounded-full bg-gold px-1.5 text-center font-mono text-[11px] leading-5 font-semibold text-gold-foreground"
    >
      {count > 99 ? "99+" : count}
    </motion.span>
  )
}
