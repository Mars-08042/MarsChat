import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type DragEvent } from "react"
import { AnimatePresence, motion } from "motion/react"
import { Link } from "react-router"
import { ArrowDown, Ban, ChevronLeft, CircleExclamationPoint, CloudUpload, PanelRight, Phone, Video, X } from "lucide-react"
import { ConnectionPill } from "@/components/common/ConnectionPill"
import { UserAvatar } from "@/components/common/UserAvatar"
import { Button, buttonVariants } from "@/components/ui/button"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { useCallStore } from "@/features/call/fake-call-store"
import { springs } from "@/lib/motion"
import { formatDateDivider } from "@/lib/time"
import { useNow } from "@/lib/use-countdown"
import { cn } from "@/lib/utils"
import { Composer } from "./Composer"
import { useChatStore } from "./fake-store"
import { formatBytes, MAX_CHAT_FILE_BYTES, MAX_THUMBNAIL_SOURCE_BYTES } from "./files"
import { toLocalFile } from "./local-file"
import { MessageRow } from "./MessageRow"
import { buildTimeline } from "./timeline"
import type { ChatMessage, Conversation } from "./types"

const EMPTY: ChatMessage[] = []
// 距离底部小于这个值视为「在底部」，新消息到达时自动跟随
const BOTTOM_THRESHOLD_PX = 80

function scrollToBottom(el: HTMLElement | null, behavior: ScrollBehavior) {
  el?.scrollTo({ top: el.scrollHeight, behavior })
}

interface Notice {
  text: string
  // 同样的提示再次出现时也要重新计时
  at: number
}

// 调用方用会话 ID 作为 key，切换会话时整体重新挂载：滚动位置、未读计数、草稿都重新开始
export function ChatView({ conversation }: { conversation: Conversation }) {
  const send = useChatStore((s) => s.send)
  const upload = useChatStore((s) => s.upload)
  const [notice, setNotice] = useState<Notice | null>(null)
  const dismissNotice = useCallback(() => setNotice(null), [])
  const blocked = conversation.blockedReason !== undefined

  // 选择、粘贴、拖放的文件都走这里：每个文件一条消息，超限的在发送前就提示（设计 3.6.9）
  const sendFiles = async (files: File[]) => {
    if (blocked) return
    const rejected: string[] = []
    for (const file of files) {
      const local = await toLocalFile(file)
      const plan = upload(conversation.id, local)
      if (!plan.ok) rejected.push(`「${file.name}」${plan.reason}`)
      // 只有按图片发送时才用本地地址显示，其余情况立即释放
      if ((!plan.ok || plan.as !== "image") && local.url) URL.revokeObjectURL(local.url)
    }
    if (rejected.length > 0) {
      const text = rejected.length === 1 ? rejected[0] : `${rejected[0]}，另有 ${rejected.length - 1} 个文件也无法发送`
      setNotice({ text, at: Date.now() })
    }
  }
  const drop = useFileDrop((files) => void sendFiles(files), blocked)

  return (
    <div className="relative flex min-h-0 flex-1 flex-col" {...drop.handlers}>
      <ChatHeader conversation={conversation} />
      <Timeline conversation={conversation} />
      <UploadNotice notice={notice} onDismiss={dismissNotice} />
      <Composer
        blockedReason={conversation.blockedReason}
        onSend={(text) => send(conversation.id, text)}
        onFiles={(files) => void sendFiles(files)}
      />
      <FileDropOverlay active={drop.active} title={conversation.title} blockedReason={conversation.blockedReason} />
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

// 发送前就被拦下的文件（超过上限、空文件），在输入框上方提示，5 秒后自动消失
function UploadNotice({ notice, onDismiss }: { notice: Notice | null; onDismiss: () => void }) {
  useEffect(() => {
    if (!notice) return
    const timer = setTimeout(onDismiss, 5000)
    return () => clearTimeout(timer)
  }, [notice, onDismiss])

  return (
    <AnimatePresence>
      {notice && (
        <motion.div
          key={notice.at}
          initial={{ opacity: 0, y: 8 }}
          animate={{ opacity: 1, y: 0 }}
          exit={{ opacity: 0, y: 8 }}
          transition={springs.smooth}
          role="alert"
          className="mx-4 flex items-center gap-2 rounded-xl border border-destructive/25 bg-destructive/10 px-3 py-2 text-sm text-destructive md:mx-6"
        >
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
        </motion.div>
      )}
    </AnimatePresence>
  )
}

// 连接状态胶囊悬浮在时间线顶部正中（顶栏高 4rem 之下）；由 ChatPage 渲染，不随会话切换重新挂载
export function PillSlot({ className }: { className?: string }) {
  const connection = useChatStore((s) => s.connection)
  const setConnection = useChatStore((s) => s.setConnection)
  return (
    <div className={cn("pointer-events-none absolute inset-x-0 top-[4.75rem] z-20 flex justify-center", className)}>
      <div className="pointer-events-auto">
        <ConnectionPill status={connection} onRetryNow={() => setConnection({ kind: "connecting" })} />
      </div>
    </div>
  )
}

// 只有单聊能通话（后端只支持一对一）；自己已在通话或振铃中时禁用（一人一通）
function CallButtons({ peerId, blocked }: { peerId: string; blocked: boolean }) {
  const peer = useChatStore((s) => s.users[peerId])
  const inCall = useCallStore((s) => s.call !== null)
  const startCall = useCallStore((s) => s.startCall)
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
                onClick={() => startCall(peer, media)}
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
      <UserAvatar seed={c.peerId ?? c.id} name={c.title} shape={c.kind === "group" ? "square" : "circle"} />
      <div className="min-w-0">
        <h2 className="truncate leading-tight font-semibold">{c.title}</h2>
        <p className="mt-0.5 text-xs text-muted-foreground">
          {c.kind === "group" ? `${c.memberCount ?? 0} 位成员` : <span className="font-mono">ID {c.peerId}</span>}
        </p>
      </div>
      <div className="ml-auto flex items-center gap-0.5">
        {c.kind === "single" && c.peerId && <CallButtons peerId={c.peerId} blocked={c.blockedReason !== undefined} />}
        <Button variant="ghost" size="icon" aria-label="会话资料">
          <PanelRight />
        </Button>
      </div>
    </header>
  )
}

function Timeline({ conversation }: { conversation: Conversation }) {
  const messages = useChatStore((s) => s.messages[conversation.id]) ?? EMPTY
  const marker = useChatStore((s) => s.unreadMarker[conversation.id])
  const me = useChatStore((s) => s.me)
  const users = useChatStore((s) => s.users)
  const retry = useChatStore((s) => s.retry)
  const cancelUpload = useChatStore((s) => s.cancelUpload)
  const now = useNow()

  const items = useMemo(() => buildTimeline(messages, marker), [messages, marker])

  const scrollRef = useRef<HTMLDivElement>(null)
  const markerRef = useRef<HTMLDivElement>(null)
  // 滚动事件里更新；消息变化后的 layout effect 读取的是新消息插入之前是否在底部
  const atBottomRef = useRef(true)
  const [atBottom, setAtBottom] = useState(true)
  // 最后一次在底部时看到的消息条数，之后到达的对方消息计入「回到底部」按钮
  const [seenCount, setSeenCount] = useState(messages.length)
  const prevCountRef = useRef(messages.length)

  // 打开会话：有未读时定位到「以下为新消息」，否则到底部
  useLayoutEffect(() => {
    if (markerRef.current) markerRef.current.scrollIntoView({ block: "center" })
    else scrollToBottom(scrollRef.current, "instant")
  }, [])

  // 新消息：自己发的总是滚到底部；对方发的只在原本就在底部时跟随
  useLayoutEffect(() => {
    if (messages.length <= prevCountRef.current) {
      prevCountRef.current = messages.length
      return
    }
    prevCountRef.current = messages.length
    const last = messages[messages.length - 1]
    if (last.senderId === me.id || atBottomRef.current) scrollToBottom(scrollRef.current, "smooth")
  }, [messages, me.id])

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
    const el = scrollRef.current
    if (!el) return
    const bottom = el.scrollHeight - el.scrollTop - el.clientHeight < BOTTOM_THRESHOLD_PX
    atBottomRef.current = bottom
    setAtBottom(bottom)
    if (bottom) setSeenCount(messages.length)
  }

  const newCount = atBottom ? 0 : messages.slice(seenCount).filter((m) => m.senderId !== me.id).length

  return (
    <div className="relative min-h-0 flex-1">
      <div ref={scrollRef} onScroll={onScroll} className="h-full overflow-y-auto pt-6 pb-4 [scrollbar-width:thin]">
        {/* 已在页面上的消息不播放入场动画，只有之后新增的才播放 */}
        <AnimatePresence initial={false}>
          {items.map((item) => {
            if (item.kind === "date") {
              return (
                <div key={item.key} className="my-5 flex justify-center">
                  <span className="rounded-full bg-muted px-3 py-1 text-xs font-medium text-muted-foreground">
                    {formatDateDivider(item.at, now)}
                  </span>
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
                conversation={conversation}
                sender={users[m.senderId]}
                mine={m.senderId === me.id}
                firstInGroup={item.firstInGroup}
                lastInGroup={item.lastInGroup}
                onRetry={(id) => retry(conversation.id, id)}
                onCancelUpload={(id) => cancelUpload(conversation.id, id)}
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
