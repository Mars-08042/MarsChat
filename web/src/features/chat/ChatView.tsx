import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type DragEvent, type ReactNode } from "react"
import { AnimatePresence, motion } from "motion/react"
import { Link } from "react-router"
import { ArrowDown, Ban, ChevronLeft, CircleExclamationPoint, CloudUpload, Hourglass, PanelRight, Phone, Video, X } from "lucide-react"
import { ConnectionPill } from "@/components/common/ConnectionPill"
import { UserAvatar } from "@/components/common/UserAvatar"
import { Button, buttonVariants } from "@/components/ui/button"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { springs } from "@/lib/motion"
import { formatTimeDivider } from "@/lib/time"
import { useNow, useSecondsUntil } from "@/lib/use-countdown"
import { cn } from "@/lib/utils"
import { callActions, chat } from "@/realtime/client"
import { toChatUser } from "@/realtime/convert"
import { useCall } from "@/stores/call"
import { useChat, type Timeline as TimelineData } from "@/stores/chat"
import { useSession } from "@/stores/session"
import { useUi } from "@/stores/ui"
import { Composer } from "./Composer"
import { ConversationPanel } from "./ConversationPanel"
import { MAX_CHAT_FILE_BYTES, MAX_THUMBNAIL_SOURCE_BYTES } from "./files"
import { formatBytes } from "@/lib/utils"
import { MessageRow } from "./MessageRow"
import { buildTimeline } from "./timeline"
import { readMarkId, type ChatMessage, type ChatUser, type Conversation } from "./types"

const EMPTY: ChatMessage[] = []
// 距离底部小于这个值视为「在底部」，新消息到达时自动跟随
const BOTTOM_THRESHOLD_PX = 80
// 距离顶部小于这个值时加载更早的一页
const LOAD_OLDER_PX = 400

function scrollToBottom(el: HTMLElement | null, behavior: ScrollBehavior) {
  el?.scrollTo({ top: el.scrollHeight, behavior })
}

function isAtBottom(el: HTMLElement) {
  return el.scrollHeight - el.scrollTop - el.clientHeight < BOTTOM_THRESHOLD_PX
}

interface Notice {
  text: string
  // 同样的提示再次出现时也要重新计时
  at: number
}

// 调用方用会话 ID 作为 key，切换会话时整体重新挂载：滚动位置、未读计数、草稿都重新开始。
// draftPeerId 有值时是和这位好友的草稿单聊，第一条消息确认后由 ChatPage 跳到真正的会话
export function ChatView({ conversation, draftPeerId }: { conversation: Conversation; draftPeerId?: string }) {
  const [notice, setNotice] = useState<Notice | null>(null)
  const dismissNotice = useCallback(() => setNotice(null), [])
  const blocked = conversation.blockedReason !== undefined

  // 选择、粘贴、拖放都走这里（设计 3.6.9）。不能上传的文件在输入框上方提示：只列第一个，说明另有几个
  const sendFiles = async (files: File[]) => {
    if (blocked || files.length === 0) return
    const rejected = await chat.sendFiles(draftPeerId ? { draftPeerId } : conversation, files)
    if (rejected.length === 0) return
    const more = rejected.length > 1 ? `，另有 ${rejected.length - 1} 个文件也没有发送` : ""
    setNotice({ text: `${rejected[0]}${more}`, at: Date.now() })
  }
  const drop = useFileDrop((files) => void sendFiles(files), blocked)

  return (
    <div className="relative flex min-h-0 flex-1">
      <div className="relative flex min-h-0 min-w-0 flex-1 flex-col" {...drop.handlers}>
        <ChatHeader conversation={conversation} />
        <Timeline conversation={conversation} draft={draftPeerId !== undefined} />
        <RateLimitNotice />
        <UploadNotice notice={notice} onDismiss={dismissNotice} />
        <Composer
          blockedReason={conversation.blockedReason}
          onSend={(text) => chat.sendText(draftPeerId ? { draftPeerId } : conversation, text)}
          onFiles={(files) => void sendFiles(files)}
        />
        <FileDropOverlay active={drop.active} title={conversation.title} blockedReason={conversation.blockedReason} />
      </div>
      <ConversationPanel conversation={conversation} />
    </div>
  )
}

// 拖入文件时显示整块的投放区域。dragenter/dragleave 在子元素之间移动时也会触发，用计数判断是否真的离开
function useFileDrop(onFiles: (files: File[]) => void, blocked: boolean) {
  const [active, setActive] = useState(false)
  const depthRef = useRef(0)
  const hasFiles = (e: DragEvent) => Array.from(e.dataTransfer.types).includes("Files")

  return {
    active,
    handlers: {
      onDragEnter: (e: DragEvent) => {
        if (!hasFiles(e)) return
        e.preventDefault()
        depthRef.current++
        setActive(true)
      },
      onDragOver: (e: DragEvent) => {
        if (!hasFiles(e)) return
        e.preventDefault()
        e.dataTransfer.dropEffect = blocked ? "none" : "copy"
      },
      onDragLeave: (e: DragEvent) => {
        if (!hasFiles(e)) return
        depthRef.current = Math.max(0, depthRef.current - 1)
        if (depthRef.current === 0) setActive(false)
      },
      onDrop: (e: DragEvent) => {
        if (!hasFiles(e)) return
        e.preventDefault()
        depthRef.current = 0
        setActive(false)
        if (!blocked) onFiles(Array.from(e.dataTransfer.files))
      },
    },
  }
}

function FileDropOverlay({ active, title, blockedReason }: { active: boolean; title: string; blockedReason?: string }) {
  return (
    <AnimatePresence>
      {active && (
        <motion.div
          initial={{ opacity: 0 }}
          animate={{ opacity: 1 }}
          exit={{ opacity: 0 }}
          transition={springs.gentle}
          // 不接收指针事件，避免覆盖层本身触发 dragleave
          className="pointer-events-none absolute inset-3 z-40 flex flex-col items-center justify-center gap-4 rounded-card border-2 border-dashed border-brand/60 bg-background/85 text-center backdrop-blur-md"
        >
          {blockedReason ? (
            <>
              <Ban className="size-10 text-muted-foreground" />
              <p className="text-sm text-muted-foreground">{blockedReason}</p>
            </>
          ) : (
            <>
              <motion.span
                initial={{ scale: 0.8, y: 8 }}
                animate={{ scale: 1, y: 0 }}
                transition={springs.snappy}
                className="flex size-16 items-center justify-center rounded-2xl text-white shadow-[0_16px_40px_-8px_rgb(226_69_44/0.6)]"
                style={{ background: "linear-gradient(135deg, #E2452C, #C2361F)" }}
              >
                <CloudUpload className="size-8" />
              </motion.span>
              <div className="space-y-1.5 px-6">
                <p className="font-display text-lg font-semibold">松开即可发送给 {title}</p>
                <p className="text-sm text-muted-foreground">
                  单个文件不超过 {formatBytes(MAX_CHAT_FILE_BYTES)}；{formatBytes(MAX_THUMBNAIL_SOURCE_BYTES)} 以内的图片以图片消息发送
                </p>
              </div>
            </>
          )}
        </motion.div>
      )}
    </AnimatePresence>
  )
}

function NoticeBar({ tone, children }: { tone: "error" | "info"; children: ReactNode }) {
  return (
    <motion.div
      initial={{ opacity: 0, y: 8 }}
      animate={{ opacity: 1, y: 0 }}
      exit={{ opacity: 0, y: 8 }}
      transition={springs.smooth}
      role={tone === "error" ? "alert" : "status"}
      className={cn(
        "mx-4 mb-2 flex items-center gap-2 rounded-xl border px-3 py-2 text-sm md:mx-6",
        tone === "error" ? "border-destructive/25 bg-destructive/10 text-destructive" : "bg-muted text-muted-foreground",
      )}
    >
      {children}
    </motion.div>
  )
}

// 发送前就被拦下的文件，在输入框上方提示，5 秒后自动消失
function UploadNotice({ notice, onDismiss }: { notice: Notice | null; onDismiss: () => void }) {
  useEffect(() => {
    if (!notice) return
    const timer = setTimeout(onDismiss, 5000)
    return () => clearTimeout(timer)
  }, [notice, onDismiss])

  return (
    <AnimatePresence>
      {notice && (
        <NoticeBar key={notice.at} tone="error">
          <CircleExclamationPoint className="size-4 shrink-0" />
          <span className="min-w-0 flex-1">{notice.text}</span>
          <button
            type="button"
            onClick={onDismiss}
            aria-label="关闭提示"
            className="flex size-6 shrink-0 items-center justify-center rounded-md transition-colors hover:bg-destructive/10"
          >
            <X className="size-3.5" />
          </button>
        </NoticeBar>
      )}
    </AnimatePresence>
  )
}

// 发消息被限流时，发送队列等服务端给出的时间后自动重发，这里显示倒计时
function RateLimitNotice() {
  const until = useChat((s) => s.rateLimitedUntil)
  return <AnimatePresence>{until !== undefined && <RateLimitCountdown key={until} until={until} />}</AnimatePresence>
}

function RateLimitCountdown({ until }: { until: number }) {
  const seconds = useSecondsUntil(until)
  if (seconds <= 0) return null
  return (
    <NoticeBar tone="info">
      <Hourglass className="size-4 shrink-0" />
      <span className="min-w-0 flex-1">发送太频繁，{seconds} 秒后自动重发</span>
    </NoticeBar>
  )
}

// 连接状态胶囊悬浮在时间线顶部正中（顶栏高 4rem 之下）；由 ChatPage 渲染，不随会话切换重新挂载
export function PillSlot({ className }: { className?: string }) {
  const connection = useChat((s) => s.connection)
  return (
    <div className={cn("pointer-events-none absolute inset-x-0 top-[4.75rem] z-20 flex justify-center", className)}>
      <div className="pointer-events-auto">
        <ConnectionPill status={connection} onRetryNow={chat.retryNow} />
      </div>
    </div>
  )
}

// 只有单聊能通话（后端只支持一对一）；自己已在通话或振铃中时禁用（一人一通）
function CallButtons({ peerId, blocked }: { peerId: string; blocked: boolean }) {
  const peer = useChat((s) => s.users[peerId])
  const inCall = useCall((s) => s.call !== null)
  const disabledReason = inCall ? "你正在通话中" : blocked ? "当前会话不能发起通话" : undefined
  if (!peer) return null

  return (
    <>
      {(["audio", "video"] as const).map((media) => (
        <Tooltip key={media}>
          <TooltipTrigger
            render={
              <Button
                variant="ghost"
                size="icon"
                aria-label={media === "audio" ? "语音通话" : "视频通话"}
                disabled={disabledReason !== undefined}
                onClick={() => callActions.start(peer, media)}
              />
            }
          >
            {media === "audio" ? <Phone /> : <Video />}
          </TooltipTrigger>
          <TooltipContent side="bottom">{disabledReason ?? (media === "audio" ? "语音通话" : "视频通话")}</TooltipContent>
        </Tooltip>
      ))}
    </>
  )
}

function ChatHeader({ conversation: c }: { conversation: Conversation }) {
  const iconButton = buttonVariants({ variant: "ghost", size: "icon" })
  return (
    <header className="z-10 flex h-16 shrink-0 items-center gap-3 border-b bg-background/80 px-3 backdrop-blur-md md:px-5">
      <Link to="/chat" aria-label="返回会话列表" className={cn(iconButton, "-ml-1 md:hidden")}>
        <ChevronLeft className="size-5" />
      </Link>
      <UserAvatar seed={c.peerId ?? c.id} name={c.title} avatarId={c.avatarId} shape={c.kind === "group" ? "square" : "circle"} />
      <div className="min-w-0">
        <h2 className="truncate leading-tight font-semibold">{c.title}</h2>
        <p className="mt-0.5 text-xs text-muted-foreground">
          {c.kind === "group" ? (c.dismissed ? "已解散" : `${c.memberCount ?? 0} 位成员`) : <span className="font-mono">ID {c.peerId}</span>}
        </p>
      </div>
      <div className="ml-auto flex items-center gap-0.5">
        {c.kind === "single" && c.peerId && <CallButtons peerId={c.peerId} blocked={c.blockedReason !== undefined} />}
        <PanelToggle conversationId={c.id} />
      </div>
    </header>
  )
}

function PanelToggle({ conversationId }: { conversationId: string }) {
  const open = useUi((s) => s.panel?.conversationId === conversationId)
  const openPanel = useUi((s) => s.openPanel)
  const closePanel = useUi((s) => s.closePanel)
  return (
    <Button
      variant="ghost"
      size="icon"
      aria-label={open ? "关闭会话资料" : "会话资料"}
      aria-pressed={open}
      onClick={() => (open ? closePanel() : openPanel(conversationId))}
      className={cn(open && "bg-muted text-foreground")}
    >
      <PanelRight />
    </Button>
  )
}

// 时间线要等最新一页和未读分隔线都准备好再挂载，挂载时一次定位到分隔线或底部。草稿单聊只有发送中的消息
function Timeline({ conversation, draft }: { conversation: Conversation; draft: boolean }) {
  const id = conversation.id
  const timeline = useChat((s) => s.timelines[id])
  const ready = useChat((s) => draft || (s.timelines[id] !== undefined && id in s.unreadMarker))
  if (ready) return <MessageList conversation={conversation} timeline={timeline} />
  return <TimelineLoading conversationId={id} />
}

// 最新一页加载失败时同步引擎不会自动重试；超过几秒还没加载出来就给出重新加载的按钮
function TimelineLoading({ conversationId }: { conversationId: string }) {
  const [slow, setSlow] = useState(false)
  const [retrying, setRetrying] = useState(false)

  useEffect(() => {
    const timer = setTimeout(() => setSlow(true), 5000)
    return () => clearTimeout(timer)
  }, [])

  return (
    <div className="flex min-h-0 flex-1 flex-col items-center justify-center gap-3 text-sm text-muted-foreground">
      <span>正在加载消息…</span>
      {slow && (
        <Button
          variant="ghost"
          size="sm"
          disabled={retrying}
          onClick={() => {
            setRetrying(true)
            void chat.open(conversationId, true).finally(() => setRetrying(false))
          }}
        >
          重新加载
        </Button>
      )}
    </div>
  )
}

function MessageList({ conversation, timeline }: { conversation: Conversation; timeline?: TimelineData }) {
  const id = conversation.id
  const outbox = useChat((s) => s.outbox[id])
  const marker = useChat((s) => s.unreadMarker[id])
  const meId = useChat((s) => s.meId)
  const users = useChat((s) => s.users)
  const me = useMeAsUser(meId)
  const now = useNow()

  const confirmed = timeline?.messages ?? EMPTY
  // 已确认的消息在前，发送中和失败的在后。推送可能比 SendAck 先到，这时同一条消息两边都有，以已确认的为准
  const messages = useMemo(() => {
    if (!outbox || outbox.length === 0) return confirmed
    const ids = new Set(confirmed.map((m) => m.id))
    return [...confirmed, ...outbox.filter((m) => !ids.has(m.id))]
  }, [confirmed, outbox])
  const items = useMemo(() => buildTimeline(messages, marker), [messages, marker])
  const readId = readMarkId(messages, conversation, meId)
  // 消息行是 memo 的，只给它会话里不常变的两个字段（types.ts 的 ConversationRef）
  const { kind, peerId } = conversation
  const rowConversation = useMemo(() => ({ kind, peerId }), [kind, peerId])

  const hasOlder = timeline?.hasOlder ?? false
  const loadingOlder = timeline?.loadingOlder ?? false
  const lastSeq = confirmed.at(-1)?.seq ?? 0
  const firstId = messages[0]?.id
  const lastId = messages.at(-1)?.id

  const scrollRef = useRef<HTMLDivElement>(null)
  const markerRef = useRef<HTMLDivElement>(null)
  // 滚动事件里更新；消息变化后的 layout effect 读取的是新消息插入之前是否在底部
  const atBottomRef = useRef(true)
  const [atBottom, setAtBottom] = useState(true)
  // 最后一次在底部时看到的最大 seq，之后到达的对方消息计入「回到底部」按钮
  const [seenSeq, setSeenSeq] = useState(lastSeq)
  const prevFirstIdRef = useRef(firstId)
  const prevLastIdRef = useRef(lastId)
  // 上一次提交后的内容高度；在顶部插入更早的消息后，按高度差恢复滚动位置
  const heightRef = useRef(0)

  const reportRead = useCallback(() => {
    if (atBottomRef.current && document.visibilityState === "visible" && document.hasFocus()) chat.reportRead(id)
  }, [id])

  const syncBottom = () => {
    const el = scrollRef.current
    if (!el) return
    const bottom = isAtBottom(el)
    atBottomRef.current = bottom
    setAtBottom(bottom)
    if (bottom) {
      setSeenSeq(lastSeq)
      if (conversation.unread > 0) reportRead()
    }
  }

  // 打开会话：有未读时定位到「以下为新消息」，否则到底部
  useLayoutEffect(() => {
    const el = scrollRef.current
    if (!el) return
    if (markerRef.current) markerRef.current.scrollIntoView({ block: "center" })
    else scrollToBottom(el, "instant")
    atBottomRef.current = isAtBottom(el)
    // 定位不一定触发 scroll 事件，下一帧按实际位置更新按钮
    const frame = requestAnimationFrame(syncBottom)
    return () => cancelAnimationFrame(frame)
    // 只在挂载时定位一次
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // 顶部插入了更早的消息：保持当前看到的内容不动（容器关闭了浏览器自带的滚动锚定）
  useLayoutEffect(() => {
    const el = scrollRef.current
    const prev = prevFirstIdRef.current
    prevFirstIdRef.current = firstId
    if (el && prev && prev !== firstId && messages.some((m) => m.id === prev)) {
      el.scrollTop += el.scrollHeight - heightRef.current
    }
  }, [firstId, messages])

  // 新消息：自己发的总是滚到底部；对方发的只在原本就在底部时跟随
  useLayoutEffect(() => {
    const prev = prevLastIdRef.current
    prevLastIdRef.current = lastId
    if (!lastId || prev === lastId) return
    const last = messages[messages.length - 1]
    if (last.senderId === meId || atBottomRef.current) scrollToBottom(scrollRef.current, "smooth")
  }, [lastId, messages, meId])

  // 「已读」出现或移动会让内容变高；原本在底部就继续贴底，不让它被输入框挡住一半
  useLayoutEffect(() => {
    if (readId && atBottomRef.current) scrollToBottom(scrollRef.current, "smooth")
  }, [readId])

  // 每次提交后记录内容高度，供下一次插入更早的消息时计算差值
  useLayoutEffect(() => {
    if (scrollRef.current) heightRef.current = scrollRef.current.scrollHeight
  })

  // 内容不足一屏或插入后仍靠近顶部时继续加载更早的消息
  useEffect(() => {
    const el = scrollRef.current
    if (el && hasOlder && !loadingOlder && el.scrollTop < LOAD_OLDER_PX) void chat.loadOlder(id)
  }, [hasOlder, loadingOlder, id, messages])

  // 新消息到达、窗口重新获得焦点或切回标签页时，如果停在底部就上报已读
  useEffect(() => {
    if (conversation.unread > 0) reportRead()
  }, [conversation.unread, lastSeq, reportRead])
  useEffect(() => {
    window.addEventListener("focus", reportRead)
    document.addEventListener("visibilitychange", reportRead)
    return () => {
      window.removeEventListener("focus", reportRead)
      document.removeEventListener("visibilitychange", reportRead)
    }
  }, [reportRead])

  // 输入框变高、手机端表情面板展开时可视区域变矮；原本在底部就继续贴底，最后几条消息不会被挡住
  useEffect(() => {
    const el = scrollRef.current
    if (!el) return
    let lastHeight = el.clientHeight
    const observer = new ResizeObserver(() => {
      if (el.clientHeight < lastHeight && atBottomRef.current) scrollToBottom(el, "instant")
      lastHeight = el.clientHeight
    })
    observer.observe(el)
    return () => observer.disconnect()
  }, [])

  const onScroll = () => {
    syncBottom()
    const el = scrollRef.current
    if (el && el.scrollTop < LOAD_OLDER_PX && hasOlder) void chat.loadOlder(id)
  }

  const newCount = atBottom ? 0 : confirmed.filter((m) => m.senderId !== meId && (m.seq ?? 0) > seenSeq).length

  return (
    <div className="relative min-h-0 flex-1">
      <div
        ref={scrollRef}
        onScroll={onScroll}
        className="h-full overflow-y-auto pt-6 pb-4 [overflow-anchor:none] [scrollbar-width:thin]"
      >
        {/* 固定高度的占位，加载提示出现、消失时不会让内容跳动 */}
        {hasOlder && (
          <div className="flex h-10 items-center justify-center px-6 text-center text-xs text-muted-foreground">
            {loadingOlder
              ? "正在加载更早的消息…"
              : timeline?.jumped
                ? "离线期间消息较多，已直接显示最新消息，向上滚动查看更早的消息"
                : null}
          </div>
        )}
        {messages.length === 0 && !hasOlder && (
          <p className="px-6 py-16 text-center text-sm text-muted-foreground">
            {conversation.blockedReason ? "还没有消息" : "还没有消息，打个招呼吧"}
          </p>
        )}
        {/* 已在页面上的消息不播放入场动画，只有之后新增的才播放 */}
        <AnimatePresence initial={false}>
          {items.map((item) => {
            if (item.kind === "time") {
              return (
                <div key={item.key} className="mt-5 mb-1 px-6 text-center text-xs text-muted-foreground">
                  {formatTimeDivider(item.at, now)}
                </div>
              )
            }
            if (item.kind === "unread") {
              return (
                <div key={item.key} ref={markerRef} className="my-5 flex items-center gap-3 px-6 text-xs font-medium text-brand-text">
                  <span className="h-px flex-1 bg-brand/30" />
                  以下为新消息
                  <span className="h-px flex-1 bg-brand/30" />
                </div>
              )
            }
            const m = item.message
            return (
              <MessageRow
                key={item.key}
                message={m}
                conversation={rowConversation}
                sender={m.senderId === meId ? me : users[m.senderId]}
                mine={m.senderId === meId}
                firstInGroup={item.firstInGroup}
                lastInGroup={item.lastInGroup}
                readMark={m.id === readId}
                onRetry={chat.retry}
                // 文件上传在第 7 阶段接入，目前没有上传中的消息
                onCancelUpload={chat.cancelUpload}
              />
            )
          })}
        </AnimatePresence>
      </div>

      <AnimatePresence>
        {!atBottom && (
          <motion.button
            type="button"
            initial={{ opacity: 0, scale: 0.8, y: 8 }}
            animate={{ opacity: 1, scale: 1, y: 0 }}
            exit={{ opacity: 0, scale: 0.8, y: 8 }}
            transition={springs.snappy}
            onClick={() => scrollToBottom(scrollRef.current, "smooth")}
            aria-label={newCount > 0 ? `回到底部，${newCount} 条新消息` : "回到底部"}
            className="absolute right-5 bottom-3 flex size-10 items-center justify-center rounded-full border bg-card text-foreground shadow-float transition-colors hover:bg-muted"
          >
            <ArrowDown className="size-5" />
            {newCount > 0 && (
              <motion.span
                key={newCount}
                initial={{ scale: 0.6 }}
                animate={{ scale: 1 }}
                transition={springs.snappy}
                className="absolute -top-2 left-1/2 h-5 min-w-5 -translate-x-1/2 rounded-full bg-gold px-1.5 text-center font-mono text-[11px] leading-5 font-semibold text-gold-foreground"
              >
                {newCount > 99 ? "99+" : newCount}
              </motion.span>
            )}
          </motion.button>
        )}
      </AnimatePresence>
    </div>
  )
}

// 自己的资料来自登录状态（GetMe），不在聊天 store 的 users 里；还没加载时显示「用户 <ID>」。
// 按整个 user 对象记忆：修改资料、换头像后 session 换成新对象，时间线里自己的头像和资料卡随之更新，
// 其他时候引用不变，不会让 memo 的消息行全部重新渲染
function useMeAsUser(meId: string): ChatUser | undefined {
  const user = useSession((s) => (s.userId === meId ? s.user : null))
  return useMemo(() => (user ? toChatUser(user) : undefined), [user])
}
