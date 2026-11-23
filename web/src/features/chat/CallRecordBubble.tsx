import { Phone, Video } from "lucide-react"
import { callRecordText } from "@/features/call/call-text"
import { cn } from "@/lib/utils"
import { callActions } from "@/realtime/client"
import { useCall } from "@/stores/call"
import { useChat } from "@/stores/chat"
import type { CallRecord, ConversationRef } from "./types"

// 通话记录气泡：图标加文案，点击按同样的类型回拨（docs/design/call-record.md 3.4）。
// 通话只有单聊，回拨的对象总是会话的对方；自己正在通话或振铃时不能点，资料还没加载时也不能点。
export function CallRecordBubble({
  record,
  mine,
  conversation,
  className,
}: {
  record: CallRecord
  mine: boolean
  conversation: ConversationRef
  className?: string
}) {
  const peer = useChat((s) => (conversation.peerId ? s.users[conversation.peerId] : undefined))
  const inCall = useCall((s) => s.call !== null)
  const text = callRecordText(record, mine)
  const label = record.media === "video" ? "视频通话" : "语音通话"
  const Icon = record.media === "video" ? Video : Phone

  return (
    <button
      type="button"
      disabled={!peer || inCall}
      onClick={() => peer && callActions.start(peer, record.media)}
      aria-label={`${label}，${text}，点击回拨`}
      title={inCall ? "你正在通话中" : `重新发起${label}`}
      className={cn(
        className,
        "inline-flex items-center gap-2 px-3.5 py-2 text-[15px] leading-relaxed transition-opacity outline-none select-none",
        "focus-visible:ring-2 focus-visible:ring-ring/50 enabled:cursor-pointer enabled:hover:opacity-90 disabled:cursor-default",
        // 我方气泡在右侧，图标放在靠头像的一侧
        mine && "flex-row-reverse",
      )}
    >
      <Icon aria-hidden="true" className="size-4 shrink-0" />
      <span>{text}</span>
    </button>
  )
}
