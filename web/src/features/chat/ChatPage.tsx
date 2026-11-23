import { useEffect, useMemo, useState } from "react"
import { Link, Navigate, useParams } from "react-router"
import { MessageCircleOff } from "lucide-react"
import { Button, buttonVariants } from "@/components/ui/button"
import { cn } from "@/lib/utils"
import { chat } from "@/realtime/client"
import { draftKey, useChat } from "@/stores/chat"
import { ChatView, PillSlot } from "./ChatView"
import { ConversationList } from "./ConversationList"
import { EmptyChat } from "./EmptyChat"
import type { Conversation } from "./types"

// /chat、/chat/:conversationId 和草稿单聊 /chat/with/:userId。
// 768px 以下只显示其中一栏：有会话时显示会话，否则显示列表
export function ChatPage() {
  const { conversationId, userId } = useParams()
  const conversation = useChat((s) => (conversationId ? s.conversations[conversationId] : undefined))
  // 最近一次打开结束的会话 ID；结束后会话仍不存在，就显示无法打开
  const [settled, setSettled] = useState<string>()

  useEffect(() => {
    let cancelled = false
    void chat.open(conversationId).then(() => {
      if (!cancelled) setSettled(conversationId)
    })
    return () => {
      cancelled = true
    }
  }, [conversationId])

  const draft = useDraft(userId)
  if (draft.redirect !== undefined) return <Navigate to={draft.redirect} replace />

  const shown = draft.conversation ?? conversation
  const missing = conversationId !== undefined && !conversation && settled === conversationId
  const occupied = shown !== undefined || conversationId !== undefined

  return (
    <>
      <ConversationList activeId={conversationId} className={cn(occupied && "max-md:hidden")} />
      <main className={cn("relative flex min-w-0 flex-1 flex-col", !occupied && "max-md:hidden")}>
        {/* 胶囊放在会话视图外面，切换会话时不重新挂载，「已同步」不会每次都重新出现 */}
        <PillSlot className={shown ? undefined : "top-4"} />
        {shown ? (
          <ChatView key={shown.id} conversation={shown} draftPeerId={draft.conversation?.peerId} />
        ) : missing ? (
          <CannotOpen onRetry={() => void chat.open(conversationId, true)} />
        ) : conversationId ? (
          <div className="flex flex-1 items-center justify-center text-sm text-muted-foreground">正在打开会话…</div>
        ) : (
          <EmptyChat />
        )}
      </main>
    </>
  )
}

// 和好友还没有会话时的草稿单聊（设计 3.5）：第一条消息确认后跳到真正的会话；已有会话时直接跳过去
function useDraft(userId: string | undefined): { conversation?: Conversation; redirect?: string } {
  const meId = useChat((s) => s.meId)
  const resolved = useChat((s) => (userId ? s.draftResolved[userId] : undefined))
  const existing = useChat((s) =>
    userId ? Object.values(s.conversations).find((c) => c.kind === "single" && c.peerId === userId)?.id : undefined,
  )
  const peer = useChat((s) => (userId ? s.users[userId] : undefined))

  // 从地址栏直接打开（刷新页面）时还没有对方的资料
  useEffect(() => {
    if (userId && !peer) void chat.loadUser(userId)
  }, [userId, peer])

  const conversation = useMemo<Conversation | undefined>(
    () =>
      userId
        ? {
            id: draftKey(userId),
            kind: "single",
            peerId: userId,
            title: peer?.nickname ?? `用户 ${userId}`,
            avatarId: peer?.avatarId,
            maxSeq: 0,
            readSeq: 0,
            peerReadSeq: 0,
            unread: 0,
          }
        : undefined,
    [userId, peer],
  )

  if (!userId) return {}
  if (userId === meId) return { redirect: "/chat" }
  const target = resolved ?? existing
  if (target) return { redirect: `/chat/${target}` }
  return { conversation }
}

function CannotOpen({ onRetry }: { onRetry: () => void }) {
  return (
    <div className="flex flex-1 flex-col items-center justify-center gap-3 px-6 text-center">
      <MessageCircleOff className="size-10 text-muted-foreground" />
      <div className="space-y-1">
        <p className="font-medium">无法打开这个会话</p>
        <p className="text-sm text-muted-foreground">会话不存在、你已不是成员，或者网络暂时不可用。</p>
      </div>
      <div className="flex gap-2">
        <Button variant="outline" onClick={onRetry}>
          重试
        </Button>
        <Link to="/chat" className={buttonVariants({ variant: "ghost" })}>
          返回会话列表
        </Link>
      </div>
    </div>
  )
}
