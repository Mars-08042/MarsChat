import { useState } from "react"
import { Link } from "react-router"
import { MessageCircle, Search } from "lucide-react"
import { toast } from "sonner"
import { errorText, isApiError } from "@/api/errors"
import { AppDialog } from "@/components/common/AppDialog"
import { CopyId } from "@/components/common/CopyId"
import { UserAvatar } from "@/components/common/UserAvatar"
import { Button, buttonVariants } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { cn } from "@/lib/utils"
import { chat } from "@/realtime/client"
import { useSession } from "@/stores/session"
import { useUi } from "@/stores/ui"
import { sendFriendRequest, useFriends, useSentRequests, useUserProfile } from "./queries"

// 用户 ID 是 snowflake，十进制最多 19 位
const USER_ID = /^\d{1,19}$/
const MESSAGE_MAX = 100

// 按用户 ID 添加好友（没有用户搜索接口，设计 2.2）：先查出对方资料确认，再填附言发送申请
export function AddFriendDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  return (
    <AppDialog open={open} onOpenChange={onOpenChange} title="添加好友" description="输入对方的用户 ID，对方可以在联系人页复制自己的 ID。">
      {open && <AddFriendBody onDone={() => onOpenChange(false)} />}
    </AppDialog>
  )
}

function AddFriendBody({ onDone }: { onDone: () => void }) {
  const meId = useSession((s) => s.userId) ?? ""
  const [input, setInput] = useState("")
  const [lookup, setLookup] = useState<string>()
  const trimmed = input.trim()
  const valid = USER_ID.test(trimmed)

  return (
    <div className="flex flex-col gap-4 overflow-y-auto px-5 pb-5">
      <form
        className="flex flex-col gap-1.5"
        onSubmit={(e) => {
          e.preventDefault()
          if (valid) setLookup(trimmed)
        }}
      >
        <Label htmlFor="friend-id">对方的用户 ID</Label>
        <div className="flex gap-2">
          <Input
            id="friend-id"
            autoFocus
            inputMode="numeric"
            autoComplete="off"
            placeholder="例如 117695561499226112"
            value={input}
            onChange={(e) => setInput(e.target.value)}
            aria-invalid={trimmed !== "" && !valid}
            className="font-mono"
          />
          <Button type="submit" variant="outline" disabled={!valid}>
            <Search />
            查找
          </Button>
        </div>
        {trimmed !== "" && !valid && <p className="text-xs text-destructive">用户 ID 只包含数字</p>}
      </form>

      {lookup === meId ? (
        <p className="rounded-lg bg-muted px-3 py-2 text-sm text-muted-foreground">这是你自己的 ID</p>
      ) : (
        lookup && <Candidate key={lookup} userId={lookup} onDone={onDone} />
      )}

      <div className="flex items-center gap-1 border-t pt-3 text-xs text-muted-foreground">
        我的
        <CopyId id={meId} />
      </div>
    </div>
  )
}

function Candidate({ userId, onDone }: { userId: string; onDone: () => void }) {
  const profile = useUserProfile(userId, true)
  const friends = useFriends()
  const sent = useSentRequests()
  const myName = useSession((s) => s.user?.nickname)
  const celebrate = useUi((s) => s.celebrate)
  const [message, setMessage] = useState(myName ? `我是${myName}` : "")
  const [sending, setSending] = useState(false)

  if (profile.isPending) return <p className="py-4 text-center text-sm text-muted-foreground">正在查找…</p>
  if (profile.isError) {
    const notFound = isApiError(profile.error) && profile.error.reason === "USER_NOT_FOUND"
    return <p className="rounded-lg bg-muted px-3 py-2 text-sm text-muted-foreground">{notFound ? "没有找到这个用户，请检查 ID" : errorText(profile.error)}</p>
  }
  const user = profile.data
  if (!user) return null

  const isFriend = friends.data?.some((f) => f.user.id === userId) ?? false
  const pending = sent.data?.some((r) => r.peer.id === userId && r.state === "pending") ?? false

  const submit = async () => {
    setSending(true)
    try {
      const { became } = await sendFriendRequest(userId, message.trim())
      if (became) {
        // 对方之前也向我发过申请，直接成为好友
        chat.setFriendBlocked(userId, undefined)
        celebrate()
        toast(`你和 ${user.nickname} 已成为好友`)
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
    <div className="flex flex-col gap-3 rounded-xl border bg-card p-4">
      <div className="flex items-center gap-3">
        <UserAvatar seed={user.id} name={user.nickname} src={user.avatarUrl} size="lg" />
        <div className="min-w-0">
          <p className="truncate font-semibold">{user.nickname}</p>
          <p className="truncate text-sm text-muted-foreground">{user.signature || "还没有填写个性签名"}</p>
        </div>
      </div>
      {isFriend ? (
        <Link to={`/chat/with/${userId}`} onClick={onDone} className={cn(buttonVariants({ size: "sm" }), "w-full")}>
          <MessageCircle />
          已经是好友，去聊天
        </Link>
      ) : pending ? (
        <p className="rounded-lg bg-muted px-3 py-2 text-center text-sm text-muted-foreground">已发送好友申请，等待对方同意</p>
      ) : (
        <form
          className="flex flex-col gap-2"
          onSubmit={(e) => {
            e.preventDefault()
            void submit()
          }}
        >
          <Label htmlFor="friend-message">附言</Label>
          <Input
            id="friend-message"
            value={message}
            maxLength={MESSAGE_MAX}
            placeholder="选填，最多 100 字"
            onChange={(e) => setMessage(e.target.value)}
          />
          <Button type="submit" disabled={sending}>
            {sending ? "发送中…" : "发送好友申请"}
          </Button>
        </form>
      )}
    </div>
  )
}
