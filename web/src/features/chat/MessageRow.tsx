import { useEffect, useState } from "react"
import { motion } from "motion/react"
import { CircleExclamationPoint, LoaderCircle, RotateCw } from "lucide-react"
import { UserAvatar } from "@/components/common/UserAvatar"
import { ProfileCard } from "@/features/contacts/ProfileCard"
import { springs } from "@/lib/motion"
import { cn } from "@/lib/utils"
import { FileAttachment, ImageAttachment } from "./attachments"
import { EmojiText } from "./EmojiText"
import type { ChatMessage, ChatUser, Conversation, SendStatus } from "./types"

// 发送中超过这个时间才显示转圈，网络正常时确认在几十毫秒内到达，不必闪一下
export const PENDING_SPINNER_DELAY_MS = 1000

interface MessageRowProps {
  message: ChatMessage
  conversation: Conversation
  sender?: ChatUser
  mine: boolean
  firstInGroup: boolean
  lastInGroup: boolean
  // 单聊里对方已读到的我方最新一条消息，在它下方显示「已读」
  readMark?: boolean
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
  readMark = false,
  onRetry,
  onCancelUpload,
}: MessageRowProps) {
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
        <ProfileCard userId={message.senderId} user={sender} conversation={conversation}>
          <UserAvatar seed={message.senderId} name={senderName} src={sender?.avatarUrl} />
        </ProfileCard>
      ) : (
        <span aria-hidden="true" className="w-10 shrink-0" />
      )}

      <div className={cn("flex max-w-[min(72%,38rem)] min-w-0 flex-col", mine ? "items-end" : "items-start")}>
        {showName && firstInGroup && <span className="mb-1 ml-1 text-xs font-medium text-muted-foreground">{senderName}</span>}
        <div className={cn("flex items-center gap-2", mine && "flex-row-reverse")}>
          <Bubble
            message={message}
            mine={mine}
            senderName={senderName}
            lastInGroup={lastInGroup}
            onCancelUpload={() => onCancelUpload(message.id)}
          />
          {mine && <SendingMark status={message.status} />}
        </div>
        {message.status === "failed" && <FailedHint reason={message.failReason} onRetry={() => onRetry(message.id)} />}
        {readMark && message.status === "sent" && (
          <motion.span
            initial={{ opacity: 0 }}
            animate={{ opacity: 1 }}
            transition={springs.smooth}
            className="mt-1 mr-1 text-[11px] leading-none text-muted-foreground"
          >
            已读
          </motion.span>
        )}
      </div>
    </motion.div>
  )
}

function Bubble({
  message,
  mine,
  senderName,
  lastInGroup,
  onCancelUpload,
}: {
  message: ChatMessage
  mine: boolean
  senderName: string
  lastInGroup: boolean
  onCancelUpload: () => void
}) {
  const { content } = message
  const shape = cn(
    "rounded-bubble",
    // 靠头像一侧的上角总是收窄，第一条像从头像引出的对话框；同一组内相邻的下角也收窄，读起来像一串
    mine && "rounded-tr-bubble-tail",
    mine && !lastInGroup && "rounded-br-bubble-tail",
    !mine && "rounded-tl-bubble-tail",
    !mine && !lastInGroup && "rounded-bl-bubble-tail",
  )

  // 图片不加气泡底色和内边距，直接按气泡形状裁切
  if (content.kind === "image") {
    return (
      <div className={cn(shape, "overflow-hidden ring-1 ring-border/60")}>
        <ImageAttachment message={message} image={content.image} senderName={senderName} onCancel={onCancelUpload} />
      </div>
    )
  }

  const colors = mine ? "bg-brand-strong text-white" : "bg-bubble-peer text-foreground"

  if (content.kind === "file") {
    return (
      <div className={cn(shape, colors, "p-3")}>
        <FileAttachment message={message} file={content.file} mine={mine} onCancel={onCancelUpload} />
      </div>
    )
  }

  return (
    <div className={cn(shape, colors, "px-3.5 py-2 text-[15px] leading-relaxed break-words whitespace-pre-wrap")}>
      <EmojiText text={content.text} />
    </div>
  )
}

// 参考微信：发送成功不显示任何标记，没有失败提示就是成功；只有确认迟迟不到时，在气泡旁显示转圈
function SendingMark({ status }: { status: SendStatus }) {
  const pending = status === "pending"
  const [slow, setSlow] = useState(false)

  useEffect(() => {
    if (!pending) return
    const timer = setTimeout(() => setSlow(true), PENDING_SPINNER_DELAY_MS)
    return () => {
      clearTimeout(timer)
      setSlow(false)
    }
  }, [pending])

  if (!pending || !slow) return null
  return (
    <span role="status" className="text-muted-foreground">
      <LoaderCircle aria-hidden="true" className="size-4 animate-spin" />
      <span className="sr-only">发送中</span>
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
