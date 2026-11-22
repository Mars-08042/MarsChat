import { useState } from "react"
import { Ban, Mars, ShieldCheck, UserMinus, Venus } from "lucide-react"
import { toast } from "sonner"
import { errorText, reasonText } from "@/api/errors"
import { ConfirmDialog } from "@/components/common/ConfirmDialog"
import { CopyId } from "@/components/common/CopyId"
import { UserAvatar } from "@/components/common/UserAvatar"
import { Button } from "@/components/ui/button"
import { chat } from "@/realtime/client"
import { useChat } from "@/stores/chat"
import { blockUser, deleteFriend, unblockUser, useBlocks, useFriends, useUserProfile } from "./queries"

// 单聊的资料面板：对方的资料，以及删除好友、拉黑、解除拉黑
export function PeerPanel({ peerId }: { peerId: string }) {
  const stored = useChat((s) => s.users[peerId])
  const profile = useUserProfile(peerId, true)
  const friends = useFriends()
  const blocks = useBlocks()
  const [confirm, setConfirm] = useState<"delete" | "block" | null>(null)
  const [unblocking, setUnblocking] = useState(false)

  const user = profile.data ?? stored
  const name = user?.nickname ?? `用户 ${peerId}`
  const isFriend = friends.data?.some((f) => f.user.id === peerId) ?? false
  const blocked = blocks.data?.some((b) => b.user.id === peerId) ?? false

  const unblock = async () => {
    setUnblocking(true)
    try {
      await unblockUser(peerId)
      chat.setFriendBlocked(peerId, isFriend ? undefined : reasonText("NOT_FRIENDS"))
      toast(`已将 ${name} 移出黑名单`)
    } catch (e) {
      toast(errorText(e))
    } finally {
      setUnblocking(false)
    }
  }

  return (
    <div className="flex flex-col gap-5">
      <div className="flex flex-col items-center gap-2 pt-2 text-center">
        <UserAvatar seed={peerId} name={name} src={user?.avatarUrl} size="xl" />
        <div className="flex items-center gap-1.5">
          <p className="font-display text-lg font-semibold break-all">{name}</p>
          {user?.gender === "male" && <Mars aria-label="男" className="size-4 text-muted-foreground" />}
          {user?.gender === "female" && <Venus aria-label="女" className="size-4 text-muted-foreground" />}
        </div>
        <CopyId id={peerId} />
      </div>
      <section className="rounded-xl bg-muted/50 p-3.5">
        <h3 className="text-sm font-medium">个性签名</h3>
        <p className="mt-1.5 text-sm break-words text-muted-foreground">{user?.signature || "还没有填写个性签名"}</p>
      </section>

      <section className="flex flex-col gap-2 border-t pt-4">
        {blocked ? (
          <Button variant="outline" disabled={unblocking} onClick={() => void unblock()}>
            <ShieldCheck />
            {unblocking ? "处理中…" : "解除拉黑"}
          </Button>
        ) : (
          <Button variant="destructive" onClick={() => setConfirm("block")}>
            <Ban />
            拉黑
          </Button>
        )}
        {isFriend && (
          <Button variant="destructive" onClick={() => setConfirm("delete")}>
            <UserMinus />
            删除好友
          </Button>
        )}
      </section>

      <ConfirmDialog
        open={confirm === "delete"}
        onOpenChange={(open) => !open && setConfirm(null)}
        title={`删除好友 ${name}`}
        description="删除后聊天记录仍然保留，但双方不能再互发消息；之后可以重新申请添加。"
        confirmLabel="删除"
        destructive
        onConfirm={async () => {
          await deleteFriend(peerId)
          chat.setFriendBlocked(peerId, reasonText("NOT_FRIENDS"))
          toast(`已删除好友 ${name}`)
        }}
      />
      <ConfirmDialog
        open={confirm === "block"}
        onOpenChange={(open) => !open && setConfirm(null)}
        title={`拉黑 ${name}`}
        description="拉黑后双方都不能发消息，也不能申请好友；好友关系不会删除，对方不会收到通知。"
        confirmLabel="拉黑"
        destructive
        onConfirm={async () => {
          await blockUser(peerId)
          chat.setFriendBlocked(peerId, reasonText("BLOCKED"))
          toast(`已拉黑 ${name}`)
        }}
      />
    </div>
  )
}
