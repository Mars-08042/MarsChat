import { AnimatePresence, motion } from "motion/react"
import { Check, CheckCheck, CircleExclamationPoint, Clock3, CloudUpload, RotateCw } from "lucide-react"
import { UserAvatar } from "@/components/common/UserAvatar"
import { springs } from "@/lib/motion"
import { formatClock } from "@/lib/time"
import { cn } from "@/lib/utils"
import { FileAttachment, ImageAttachment } from "./attachments"
import { displayStatus, type ChatMessage, type ChatUser, type Conversation, type DisplayStatus } from "./types"

interface MessageRowProps {
  message: ChatMessage
  conversation: Conversation
  sender?: ChatUser
  mine: boolean
  firstInGroup: boolean
  lastInGroup: boolean
  onRetry: (messageId: string) => void
  onCancelUpload: (messageId: string) => void
}

export function MessageRow({
  message,
  conversation,
  sender,
  mine,
  firstInGroup,
  lastInGroup,
  onRetry,
  onCancelUpload,
}: MessageRowProps) {
  const status = displayStatus(message, conversation)
  // 仿微信：单聊和群聊里双方都显示头像，每组只在第一条消息旁显示一次、顶部对齐；昵称只在群聊里显示（设计 3.6.3）
  const showName = conversation.kind === "group" && !mine
  const senderName = sender?.nickname ?? `用户 ${message.senderId}`

  return (
    <motion.div
      // 我方气泡从输入框方向轻微上移并淡入，对方气泡只淡入（设计 3.6.3）
      initial={mine ? { opacity: 0, y: 10 } : { opacity: 0 }}
      animate={{ opacity: 1, y: 0 }}
      transition={springs.smooth}
      // 我方用 row-reverse，头像在右侧
      className={cn("flex items-start gap-2.5 px-4 md:px-6", mine && "flex-row-reverse", firstInGroup ? "mt-4" : "mt-1")}
    >
      {firstInGroup ? (
        <UserAvatar seed={message.senderId} name={senderName} src={sender?.avatarUrl} className="shrink-0" />
      ) : (
        <span aria-hidden="true" className="w-10 shrink-0" />
      )}

      <div className={cn("flex max-w-[min(72%,38rem)] min-w-0 flex-col", mine ? "items-end" : "items-start")}>
        {showName && firstInGroup && <span className="mb-1 ml-1 text-xs font-medium text-muted-foreground">{senderName}</span>}
        <Bubble
          message={message}
          mine={mine}
          status={status}
          senderName={senderName}
          lastInGroup={lastInGroup}
          onCancelUpload={() => onCancelUpload(message.id)}
        />
        {status === "failed" && <FailedHint reason={message.failReason} onRetry={() => onRetry(message.id)} />}
      </div>
    </motion.div>
  )
}

function Bubble({
  message,
  mine,
  status,
  senderName,
  lastInGroup,
  onCancelUpload,
}: {
  message: ChatMessage
  mine: boolean
  status: DisplayStatus
  senderName: string
  lastInGroup: boolean
  onCancelUpload: () => void
}) {
  const { content } = message
  const footer = (
    <>
      {formatClock(message.sentAt)}
      {mine && <StatusIcon status={status} />}
    </>
  )
  const shape = cn(
    "rounded-bubble",
    // 靠头像一侧的上角总是收窄，第一条像从头像引出的对话框；同一组内相邻的下角也收窄，读起来像一串
    mine && "rounded-tr-bubble-tail",
    mine && !lastInGroup && "rounded-br-bubble-tail",
    !mine && "rounded-tl-bubble-tail",
    !mine && !lastInGroup && "rounded-bl-bubble-tail",
    status === "pending" && "opacity-70",
  )

  // 图片不加气泡底色和内边距，直接按气泡形状裁切
  if (content.kind === "image") {
    return (
      <div className={cn(shape, "overflow-hidden ring-1 ring-border/60 transition-opacity")}>
        <ImageAttachment message={message} image={content.image} senderName={senderName} footer={footer} onCancel={onCancelUpload} />
      </div>
    )
  }

  const colors = mine ? "bg-brand-strong text-white" : "bg-bubble-peer text-foreground"

  if (content.kind === "file") {
    return (
      <div className={cn(shape, colors, "px-3 pt-3 pb-2 transition-opacity")}>
        <FileAttachment message={message} file={content.file} mine={mine} footer={footer} onCancel={onCancelUpload} />
      </div>
    )
  }

  return (
    <div className={cn(shape, colors, "px-3.5 py-2 text-[15px] leading-relaxed break-words whitespace-pre-wrap transition-opacity")}>
      {content.text}
      {/* 时间和状态浮在最后一行右侧；白字透明度不低于 90%，在 #C2361F 上对比度约 4.7 */}
      <span
        className={cn(
          "float-right mt-1.5 ml-3 inline-flex translate-y-0.5 items-center gap-1 font-mono text-[11px] leading-none select-none",
          mine ? "text-white/90" : "text-muted-foreground",
        )}
      >
        {footer}
      </span>
    </div>
  )
}

const STATUS_LABEL: Record<DisplayStatus, string> = {
  uploading: "上传中",
  pending: "发送中",
  sent: "已发送",
  read: "已读",
  failed: "发送失败",
}

const STATUS_ICON: Partial<Record<DisplayStatus, typeof Check>> = {
  uploading: CloudUpload,
  pending: Clock3,
  sent: Check,
  read: CheckCheck,
}

function StatusIcon({ status }: { status: DisplayStatus }) {
  const Icon = STATUS_ICON[status]
  return (
    <span className="relative inline-flex size-3.5" title={STATUS_LABEL[status]}>
      <span className="sr-only">{STATUS_LABEL[status]}</span>
      <AnimatePresence mode="popLayout" initial={false}>
        {Icon && (
          <motion.span
            key={status}
            initial={{ scale: 0.4, opacity: 0 }}
            animate={{ scale: 1, opacity: 1 }}
            exit={{ scale: 0.4, opacity: 0 }}
            transition={springs.snappy}
            className="absolute inset-0"
          >
            <Icon className="size-3.5" strokeWidth={2.5} />
          </motion.span>
        )}
      </AnimatePresence>
    </span>
  )
}

// 失败显示在气泡外侧：图标、原因和重试按钮；错误色也是红色，所以必须配图标和文字（设计 3.4.2）
function FailedHint({ reason, onRetry }: { reason?: string; onRetry: () => void }) {
  return (
    <motion.div
      initial={{ opacity: 0, y: -4 }}
      animate={{ opacity: 1, y: 0 }}
      transition={springs.smooth}
      className="mt-1 flex items-center gap-1.5 text-xs text-destructive"
    >
      <CircleExclamationPoint className="size-3.5 shrink-0" />
      <span>{reason ?? "发送失败"}</span>
      <button
        type="button"
        onClick={onRetry}
        className="ml-1 inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 font-medium transition-colors outline-none hover:bg-destructive/10 focus-visible:ring-2 focus-visible:ring-destructive/40"
      >
        <RotateCw className="size-3" />
        重试
      </button>
    </motion.div>
  )
}
