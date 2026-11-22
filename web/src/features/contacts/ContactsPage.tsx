import { useMemo, useState, type ReactNode } from "react"
import { AnimatePresence, motion } from "motion/react"
import { Link, NavLink, useLocation, useNavigate } from "react-router"
import { Ban, MessageCircle, Search, UserMinus, UserPlus } from "lucide-react"
import { toast } from "sonner"
import { errorText, reasonText } from "@/api/errors"
import { ActionMenu } from "@/components/common/ActionMenu"
import { ConfirmDialog } from "@/components/common/ConfirmDialog"
import { CopyId } from "@/components/common/CopyId"
import { ListState } from "@/components/common/ListState"
import { UserAvatar } from "@/components/common/UserAvatar"
import { Button, buttonVariants } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import type { ChatUser } from "@/features/chat/types"
import { springs } from "@/lib/motion"
import { formatListTime } from "@/lib/time"
import { useNow } from "@/lib/use-countdown"
import { cn } from "@/lib/utils"
import { chat } from "@/realtime/client"
import { useSession } from "@/stores/session"
import { useUi } from "@/stores/ui"
import { AddFriendDialog } from "./AddFriendDialog"
import { ProfileCard } from "./ProfileCard"
import {
  acceptFriendRequest,
  blockUser,
  deleteFriend,
  rejectFriendRequest,
  unblockUser,
  useBlocks,
  useFriends,
  useReceivedRequests,
  useSentRequests,
  type FriendRequestItem,
  type RequestState,
} from "./queries"

type Tab = "friends" | "requests" | "blocked"

const TABS: { tab: Tab; to: string; label: string }[] = [
  { tab: "friends", to: "/contacts", label: "好友" },
  { tab: "requests", to: "/contacts/requests", label: "申请" },
  { tab: "blocked", to: "/contacts/blocked", label: "黑名单" },
]

function tabOf(pathname: string): Tab {
  if (pathname.startsWith("/contacts/requests")) return "requests"
  if (pathname.startsWith("/contacts/blocked")) return "blocked"
  return "friends"
}

// 联系人：好友、申请、黑名单三个页签对应三个路由（设计 3.5）。加好友只能按用户 ID（设计 2.2），
// 所以把「我的 ID」放在页头，一键复制
export function ContactsPage() {
  const tab = tabOf(useLocation().pathname)
  const [adding, setAdding] = useState(false)
  const meId = useSession((s) => s.userId) ?? ""
  const pending = useReceivedRequests().data?.length ?? 0

  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-y-auto [scrollbar-width:thin]">
      <div className="mx-auto w-full max-w-2xl px-4 pt-6 pb-10 md:px-8 md:pt-10">
        <header className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <h1 className="font-display text-2xl font-semibold">联系人</h1>
            <div className="mt-1 flex items-center gap-1 text-sm text-muted-foreground">
              我的
              <CopyId id={meId} />
            </div>
          </div>
          <Button onClick={() => setAdding(true)}>
            <UserPlus />
            添加好友
          </Button>
        </header>

        <nav aria-label="联系人分类" className="mt-6 flex w-fit gap-1 rounded-xl bg-muted p-1">
          {TABS.map((t) => (
            <NavLink
              key={t.tab}
              to={t.to}
              end
              className={cn(
                "relative rounded-lg px-4 py-1.5 text-sm font-medium transition-colors outline-none focus-visible:ring-2 focus-visible:ring-ring",
                tab === t.tab ? "text-foreground" : "text-muted-foreground hover:text-foreground",
              )}
            >
              {tab === t.tab && (
                <motion.span layoutId="contacts-tab" transition={springs.smooth} className="absolute inset-0 rounded-lg bg-background shadow-sm" />
              )}
              <span className="relative inline-flex items-center gap-1.5">
                {t.label}
                {t.tab === "requests" && pending > 0 && (
                  <span className="min-w-4 rounded-full bg-gold px-1 text-center font-mono text-[10px] leading-4 font-semibold text-gold-foreground">
                    {pending > 99 ? "99+" : pending}
                  </span>
                )}
              </span>
            </NavLink>
          ))}
        </nav>

        <div className="mt-5">
          {tab === "friends" && <FriendsTab onAdd={() => setAdding(true)} />}
          {tab === "requests" && <RequestsTab />}
          {tab === "blocked" && <BlockedTab />}
        </div>
      </div>
      <AddFriendDialog open={adding} onOpenChange={setAdding} />
    </div>
  )
}

function UserRow({ user, children, sub }: { user: ChatUser; sub?: ReactNode; children?: ReactNode }) {
  return (
    <div className="flex items-center gap-3 rounded-xl px-3 py-2.5 transition-colors hover:bg-muted/60">
      <ProfileCard userId={user.id} user={user}>
        <UserAvatar seed={user.id} name={user.nickname} src={user.avatarUrl} />
      </ProfileCard>
      <div className="min-w-0 flex-1">
        <p className="truncate font-medium">{user.nickname}</p>
        <div className="truncate text-xs text-muted-foreground">{sub ?? (user.signature || <span className="font-mono">ID {user.id}</span>)}</div>
      </div>
      {children}
    </div>
  )
}

type Pending = { kind: "delete" | "block"; user: ChatUser } | null

function FriendsTab({ onAdd }: { onAdd: () => void }) {
  const friends = useFriends()
  const [query, setQuery] = useState("")
  const [confirm, setConfirm] = useState<Pending>(null)

  const shown = useMemo(() => {
    const keyword = query.trim().toLowerCase()
    const list = friends.data ?? []
    return keyword ? list.filter((f) => f.user.nickname.toLowerCase().includes(keyword) || f.user.id === keyword) : list
  }, [friends.data, query])

  const total = friends.data?.length ?? 0
  return (
    <>
      {total > 0 && (
        <div className="relative mb-3">
          <Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder={`在 ${total} 位好友中搜索昵称或 ID`}
            aria-label="搜索好友"
            className="h-9 rounded-lg border-transparent bg-muted pl-9 dark:bg-muted"
          />
        </div>
      )}
      <ListState
        query={friends}
        empty={
          total === 0 ? (
            <div className="flex flex-col items-center gap-3">
              <p>还没有好友，把你的 ID 发给朋友，或者按 ID 添加对方</p>
              <Button variant="outline" size="sm" onClick={onAdd}>
                <UserPlus />
                添加好友
              </Button>
            </div>
          ) : shown.length === 0 ? (
            "没有找到相关好友"
          ) : undefined
        }
      >
        <ul className="space-y-0.5">
          <AnimatePresence initial={false}>
            {shown.map((f) => (
              <motion.li key={f.user.id} layout exit={{ opacity: 0, x: -24 }} transition={springs.smooth}>
                <UserRow user={f.user}>
                  <Link
                    to={`/chat/with/${f.user.id}`}
                    aria-label={`和 ${f.user.nickname} 聊天`}
                    className={cn(buttonVariants({ variant: "ghost", size: "icon-sm" }), "text-muted-foreground")}
                  >
                    <MessageCircle />
                  </Link>
                  <ActionMenu
                    label={`${f.user.nickname} 的更多操作`}
                    items={[
                      { label: "删除好友", icon: UserMinus, destructive: true, onSelect: () => setConfirm({ kind: "delete", user: f.user }) },
                      { label: "拉黑", icon: Ban, destructive: true, onSelect: () => setConfirm({ kind: "block", user: f.user }) },
                    ]}
                  />
                </UserRow>
              </motion.li>
            ))}
          </AnimatePresence>
        </ul>
      </ListState>

      <ConfirmDialog
        open={confirm?.kind === "delete"}
        onOpenChange={(open) => !open && setConfirm(null)}
        title={`删除好友 ${confirm?.user.nickname ?? ""}`}
        description="删除后聊天记录仍然保留，但双方不能再互发消息；之后可以重新申请添加。"
        confirmLabel="删除"
        destructive
        onConfirm={async () => {
          if (!confirm) return
          await deleteFriend(confirm.user.id)
          chat.setFriendBlocked(confirm.user.id, reasonText("NOT_FRIENDS"))
          toast(`已删除好友 ${confirm.user.nickname}`)
        }}
      />
      <ConfirmDialog
        open={confirm?.kind === "block"}
        onOpenChange={(open) => !open && setConfirm(null)}
        title={`拉黑 ${confirm?.user.nickname ?? ""}`}
        description="拉黑后双方都不能发消息，也不能申请好友；好友关系不会删除，对方不会收到通知。可以在黑名单里解除。"
        confirmLabel="拉黑"
        destructive
        onConfirm={async () => {
          if (!confirm) return
          await blockUser(confirm.user.id)
          chat.setFriendBlocked(confirm.user.id, reasonText("BLOCKED"))
          toast(`已拉黑 ${confirm.user.nickname}`)
        }}
      />
    </>
  )
}

function RequestsTab() {
  const received = useReceivedRequests()
  const sent = useSentRequests()
  return (
    <div className="space-y-8">
      <section>
        <h2 className="mb-2 px-1 text-sm font-medium text-muted-foreground">收到的申请</h2>
        <ListState query={received} empty={received.data?.length === 0 ? "没有待处理的申请" : undefined}>
          <ul className="space-y-2">
            <AnimatePresence initial={false}>
              {received.data?.map((r) => (
                // 同意或拒绝后卡片带动画移出（设计 3.6.7）
                <motion.li
                  key={r.id}
                  layout
                  initial={{ opacity: 0, y: 8 }}
                  animate={{ opacity: 1, y: 0 }}
                  exit={{ opacity: 0, x: 40, transition: { duration: 0.2 } }}
                  transition={springs.smooth}
                >
                  <ReceivedCard request={r} />
                </motion.li>
              ))}
            </AnimatePresence>
          </ul>
        </ListState>
      </section>
      <section>
        <h2 className="mb-2 px-1 text-sm font-medium text-muted-foreground">发出的申请</h2>
        <ListState query={sent} empty={sent.data?.length === 0 ? "还没有发出过申请" : undefined}>
          <ul className="space-y-0.5">
            {sent.data?.map((r) => (
              <li key={r.id}>
                <UserRow user={r.peer} sub={r.message ? `附言：${r.message}` : undefined}>
                  <StateChip state={r.state} />
                </UserRow>
              </li>
            ))}
          </ul>
        </ListState>
      </section>
    </div>
  )
}

function ReceivedCard({ request: r }: { request: FriendRequestItem }) {
  const now = useNow()
  const [busy, setBusy] = useState<"accept" | "reject" | null>(null)
  const celebrate = useUi((s) => s.celebrate)
  const navigate = useNavigate()

  const act = async (kind: "accept" | "reject") => {
    setBusy(kind)
    try {
      if (kind === "accept") {
        await acceptFriendRequest(r.id)
        chat.setFriendBlocked(r.peer.id, undefined)
        celebrate()
        toast(`你和 ${r.peer.nickname} 已成为好友`, {
          action: { label: "发消息", onClick: () => void navigate(`/chat/with/${r.peer.id}`) },
        })
      } else {
        await rejectFriendRequest(r.id)
      }
    } catch (e) {
      toast(errorText(e))
      setBusy(null)
    }
  }

  return (
    <div className="flex items-start gap-3 rounded-xl border bg-card p-3.5">
      <ProfileCard userId={r.peer.id} user={r.peer}>
        <UserAvatar seed={r.peer.id} name={r.peer.nickname} src={r.peer.avatarUrl} />
      </ProfileCard>
      <div className="min-w-0 flex-1">
        <div className="flex items-baseline justify-between gap-2">
          <p className="truncate font-medium">{r.peer.nickname}</p>
          <span className="shrink-0 text-xs text-muted-foreground">{formatListTime(r.updatedAt, now)}</span>
        </div>
        <p className="mt-0.5 text-sm break-words text-muted-foreground">{r.message || "请求添加你为好友"}</p>
        <div className="mt-3 flex justify-end gap-2">
          <Button variant="ghost" size="sm" disabled={busy !== null} onClick={() => void act("reject")}>
            {busy === "reject" ? "处理中…" : "拒绝"}
          </Button>
          <Button size="sm" disabled={busy !== null} onClick={() => void act("accept")}>
            {busy === "accept" ? "处理中…" : "同意"}
          </Button>
        </div>
      </div>
    </div>
  )
}

const STATE_LABEL: Record<RequestState, string> = {
  pending: "等待验证",
  accepted: "已同意",
  rejected: "已拒绝",
  expired: "已失效",
}

function StateChip({ state }: { state: RequestState }) {
  return (
    <span
      className={cn(
        "shrink-0 rounded-full px-2 py-0.5 text-xs",
        state === "pending" && "bg-gold/15 text-foreground",
        state === "accepted" && "bg-success/10 text-success",
        (state === "rejected" || state === "expired") && "bg-muted text-muted-foreground",
      )}
    >
      {STATE_LABEL[state]}
    </span>
  )
}

function BlockedTab() {
  const blocks = useBlocks()
  const friends = useFriends()
  const [busy, setBusy] = useState<string | null>(null)

  const unblock = async (user: ChatUser) => {
    setBusy(user.id)
    try {
      await unblockUser(user.id)
      const isFriend = friends.data?.some((f) => f.user.id === user.id) ?? false
      chat.setFriendBlocked(user.id, isFriend ? undefined : reasonText("NOT_FRIENDS"))
      toast(`已将 ${user.nickname} 移出黑名单`)
    } catch (e) {
      toast(errorText(e))
    } finally {
      setBusy(null)
    }
  }

  return (
    <ListState query={blocks} empty={blocks.data?.length === 0 ? "黑名单是空的" : undefined}>
      <ul className="space-y-0.5">
        <AnimatePresence initial={false}>
          {blocks.data?.map((b) => (
            <motion.li key={b.user.id} layout exit={{ opacity: 0, x: -24 }} transition={springs.smooth}>
              <UserRow user={b.user}>
                <Button variant="outline" size="sm" disabled={busy === b.user.id} onClick={() => void unblock(b.user)}>
                  {busy === b.user.id ? "处理中…" : "解除拉黑"}
                </Button>
              </UserRow>
            </motion.li>
          ))}
        </AnimatePresence>
      </ul>
    </ListState>
  )
}
