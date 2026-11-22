import { useState, type ReactNode } from "react"
import { Popover } from "@base-ui/react/popover"
import { AnimatePresence, motion } from "motion/react"
import { Link } from "react-router"
import { Mars, MessageCircle, UserPlus, Venus } from "lucide-react"
import { toast } from "sonner"
import { errorText } from "@/api/errors"
import { CopyId } from "@/components/common/CopyId"
import { UserAvatar } from "@/components/common/UserAvatar"
import { Button, buttonVariants } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import type { ChatUser, Conversation } from "@/features/chat/types"
import { springs } from "@/lib/motion"
import { cn } from "@/lib/utils"
import { chat } from "@/realtime/client"
import { useChat } from "@/stores/chat"
import { useSession } from "@/stores/session"
import { useUi } from "@/stores/ui"
import { sendFriendRequest, useFriends, useSentRequests, useUserProfile } from "./queries"

const MESSAGE_MAX = 100

interface ProfileCardProps {
  userId: string
  // store 里已有的资料，打开前先用它渲染头像
  user?: ChatUser
  // 在哪个会话里打开：单聊里点对方头像时不再显示「发消息」
  conversation?: Conversation
  children: ReactNode
}

// 点头像弹出的简单资料卡：头像、昵称、性别、ID、签名，以及发消息或加好友
export function ProfileCard({ userId, user, conversation, children }: ProfileCardProps) {
  const [open, setOpen] = useState(false)
  const name = user?.nickname ?? `用户 ${userId}`

  return (
    <Popover.Root open={open} onOpenChange={setOpen}>
      <Popover.Trigger
        aria-label={`查看 ${name} 的资料`}
        className="shrink-0 cursor-pointer rounded-full outline-none focus-visible:ring-2 focus-visible:ring-ring/60 focus-visible:ring-offset-2 focus-visible:ring-offset-background"
      >
        {children}
      </Popover.Trigger>
      <AnimatePresence>
        {open && (
          <Popover.Portal keepMounted>
            <Popover.Positioner side="bottom" align="start" sideOffset={8} collisionPadding={12} className="z-50">
              <Popover.Popup
                aria-label={`${name} 的资料`}
                render={
                  <motion.div
                    initial={{ opacity: 0, y: -6, scale: 0.97 }}
                    animate={{ opacity: 1, y: 0, scale: 1 }}
                    exit={{ opacity: 0, y: -6, scale: 0.97 }}
                    transition={springs.snappy}
                    style={{ transformOrigin: "var(--transform-origin)" }}
                    className="w-72 rounded-card border bg-popover p-4 text-popover-foreground shadow-float outline-none"
                  />
                }
              >
                <CardContent userId={userId} fallback={user} conversation={conversation} onDone={() => setOpen(false)} />
              </Popover.Popup>
            </Popover.Positioner>
          </Popover.Portal>
        )}
      </AnimatePresence>
    </Popover.Root>
  )
}

// 查询和路由只在卡片打开后才用到，放在内层组件里，时间线渲染几百个头像时不会各自发请求
function CardContent({
  userId,
  fallback,
  conversation,
  onDone,
}: {
  userId: string
  fallback?: ChatUser
  conversation?: Conversation
  onDone: () => void
}) {
  const meId = useChat((s) => s.meId)
  const me = userId === meId
  const profile = useUserProfile(userId, true)
  const user = profile.data ?? fallback
  const name = user?.nickname ?? `用户 ${userId}`

  return (
    <div className="flex flex-col gap-3">
      <div className="flex items-center gap-3">
        <UserAvatar seed={userId} name={name} src={user?.avatarUrl} size="lg" />
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-1.5">
            <span className="truncate text-base font-semibold">{name}</span>
            {user?.gender === "male" && <Mars aria-label="男" className="size-4 shrink-0 text-muted-foreground" />}
            {user?.gender === "female" && <Venus aria-label="女" className="size-4 shrink-0 text-muted-foreground" />}
            {me && <span className="shrink-0 rounded-full bg-muted px-1.5 py-0.5 text-[11px] text-muted-foreground">我</span>}
          </div>
          <CopyId id={userId} className="-ml-1.5" />
        </div>
      </div>

      <p className={cn("text-sm break-words", user?.signature ? "text-foreground/85" : "text-muted-foreground")}>
        {user?.signature || "还没有填写个性签名"}
      </p>

      {!me && <Actions userId={userId} name={name} conversation={conversation} onDone={onDone} />}
    </div>
  )
}

function Actions({ userId, name, conversation, onDone }: { userId: string; name: string; conversation?: Conversation; onDone: () => void }) {
  const friends = useFriends()
  const sent = useSentRequests()
  const [composing, setComposing] = useState(false)

  if (friends.isPending) return <div className="h-8" aria-hidden="true" />

  const isFriend = friends.data?.some((f) => f.user.id === userId) ?? false
  if (isFriend) {
    // 已经在和对方单聊时不需要「发消息」
    if (conversation?.kind === "single" && conversation.peerId === userId) return null
    return (
      <Link to={`/chat/with/${userId}`} onClick={onDone} className={cn(buttonVariants({ size: "sm" }), "w-full")}>
        <MessageCircle />
        发消息
      </Link>
    )
  }

  const pending = sent.data?.some((r) => r.peer.id === userId && r.state === "pending") ?? false
  if (pending) {
    return (
      <Button size="sm" variant="secondary" disabled className="w-full">
        已发送好友申请，等待对方同意
      </Button>
    )
  }

  if (!composing) {
    return (
      <Button size="sm" className="w-full" onClick={() => setComposing(true)}>
        <UserPlus />
        加好友
      </Button>
    )
  }
  return <RequestForm userId={userId} name={name} onDone={onDone} />
}

// 附言默认「我是<我的昵称>」，可以修改；对方已经向我发过申请时直接成为好友
function RequestForm({ userId, name, onDone }: { userId: string; name: string; onDone: () => void }) {
  const myName = useSession((s) => s.user?.nickname)
  const celebrate = useUi((s) => s.celebrate)
  const [message, setMessage] = useState(myName ? `我是${myName}` : "")
  const [sending, setSending] = useState(false)

  const submit = async () => {
    setSending(true)
    try {
      const { became } = await sendFriendRequest(userId, message.trim())
      if (became) {
        // 对方之前也向我发过申请，直接成为好友
        chat.setFriendBlocked(userId, undefined)
        celebrate()
        toast(`你和 ${name} 已成为好友`)
      } else {
        toast("好友申请已发送", { description: "对方同意后会通知你" })
      }
      onDone()
    } catch (e) {
      toast(errorText(e))
      setSending(false)
    }
  }

  return (
    <motion.form
      initial={{ opacity: 0, height: 0 }}
      animate={{ opacity: 1, height: "auto" }}
      transition={springs.smooth}
      onSubmit={(e) => {
        e.preventDefault()
        void submit()
      }}
      className="flex flex-col gap-2"
    >
      <Input
        autoFocus
        aria-label="附言"
        placeholder="附言（选填）"
        value={message}
        maxLength={MESSAGE_MAX}
        onChange={(e) => setMessage(e.target.value)}
      />
      <Button type="submit" size="sm" disabled={sending} className="w-full">
        {sending ? "发送中…" : "发送好友申请"}
      </Button>
    </motion.form>
  )
}
