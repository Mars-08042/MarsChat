import { useEffect } from "react"
import { useParams } from "react-router"
import { cn } from "@/lib/utils"
import { ChatView, PillSlot } from "./ChatView"
import { ConversationList } from "./ConversationList"
import { EmptyChat } from "./EmptyChat"
import { useChatStore } from "./fake-store"

// /chat 和 /chat/:conversationId。768px 以下只显示其中一栏：有会话时显示会话，否则显示列表
export function ChatPage() {
  const { conversationId } = useParams()
  const open = useChatStore((s) => s.open)
  const conversation = useChatStore((s) => (conversationId ? s.conversations[conversationId] : undefined))

  useEffect(() => {
    open(conversationId)
  }, [conversationId, open])

  return (
    <>
      <ConversationList activeId={conversationId} className={cn(conversation && "max-md:hidden")} />
      <main className={cn("relative flex min-w-0 flex-1 flex-col", !conversation && "max-md:hidden")}>
        {/* 胶囊放在会话视图外面，切换会话时不重新挂载，「已同步」不会每次都重新出现 */}
        <PillSlot className={conversation ? undefined : "top-4"} />
        {conversation ? <ChatView key={conversation.id} conversation={conversation} /> : <EmptyChat />}
      </main>
    </>
  )
}
