import { AnimatePresence, motion } from "motion/react"
import { X } from "lucide-react"
import { Button } from "@/components/ui/button"
import { PeerPanel } from "@/features/contacts/PeerPanel"
import { GroupPanel } from "@/features/groups/GroupPanel"
import { springs } from "@/lib/motion"
import { useMediaQuery } from "@/lib/use-media-query"
import { useUi } from "@/stores/ui"
import type { Conversation } from "./types"

// 宽屏时面板和时间线并排，否则盖在时间线上（会话列表、导航栏、面板同时出现需要约 1280px）
const SIDE_BY_SIDE = "(min-width: 1280px)"
const WIDTH = 360

// 会话右侧的资料面板（设计 3.5：是界面状态，不占路由）：群聊显示群资料、成员、申请，单聊显示对方资料
export function ConversationPanel({ conversation }: { conversation: Conversation }) {
  const open = useUi((s) => s.panel?.conversationId === conversation.id)
  const close = useUi((s) => s.closePanel)
  const wide = useMediaQuery(SIDE_BY_SIDE)
  const title = conversation.kind === "group" ? "群资料" : "聊天资料"

  const body = (
    <div className="flex h-full flex-col bg-background">
      <div className="flex h-16 shrink-0 items-center justify-between border-b px-4">
        <h2 className="font-semibold">{title}</h2>
        <Button variant="ghost" size="icon" aria-label="关闭资料面板" onClick={close}>
          <X />
        </Button>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto p-4 [scrollbar-width:thin]">
        {conversation.kind === "group" ? (
          <GroupPanel conversation={conversation} />
        ) : (
          conversation.peerId && <PeerPanel peerId={conversation.peerId} />
        )}
      </div>
    </div>
  )

  if (wide) {
    return (
      <AnimatePresence initial={false}>
        {open && (
          <motion.aside
            aria-label={title}
            initial={{ width: 0, opacity: 0 }}
            animate={{ width: WIDTH, opacity: 1 }}
            exit={{ width: 0, opacity: 0 }}
            transition={springs.smooth}
            className="shrink-0 overflow-hidden border-l"
          >
            <div style={{ width: WIDTH }} className="h-full">
              {body}
            </div>
          </motion.aside>
        )}
      </AnimatePresence>
    )
  }

  return (
    <AnimatePresence>
      {open && (
        <>
          <motion.div
            key="backdrop"
            initial={{ opacity: 0 }}
            animate={{ opacity: 1 }}
            exit={{ opacity: 0 }}
            onClick={close}
            className="absolute inset-0 z-30 bg-black/30 max-sm:hidden"
          />
          <motion.aside
            key="panel"
            aria-label={title}
            initial={{ x: "100%" }}
            animate={{ x: 0 }}
            exit={{ x: "100%" }}
            transition={springs.smooth}
            className="absolute inset-y-0 right-0 z-40 w-full border-l shadow-float sm:w-[360px]"
          >
            {body}
          </motion.aside>
        </>
      )}
    </AnimatePresence>
  )
}
