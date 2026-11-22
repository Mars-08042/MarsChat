import { useMemo, useState } from "react"
import { AnimatePresence, motion } from "motion/react"
import { Crown, MicOff, ShieldCheck, ShieldOff, UserMinus, UserPlus, Volume2 } from "lucide-react"
import { toast } from "sonner"
import { errorText } from "@/api/errors"
import { ActionMenu, type ActionItem } from "@/components/common/ActionMenu"
import { AppDialog } from "@/components/common/AppDialog"
import { ConfirmDialog } from "@/components/common/ConfirmDialog"
import { ListState } from "@/components/common/ListState"
import { UserAvatar } from "@/components/common/UserAvatar"
import { Button } from "@/components/ui/button"
import type { Conversation } from "@/features/chat/types"
import { ProfileCard } from "@/features/contacts/ProfileCard"
import { springs } from "@/lib/motion"
import { formatRemaining } from "@/lib/time"
import { useNow } from "@/lib/use-countdown"
import { cn } from "@/lib/utils"
import { useSession } from "@/stores/session"
import { FriendPicker } from "./FriendPicker"
import { RoleBadge } from "./RoleBadge"
import { canManage, isManager, sortMembers, type GroupInfo, type MemberItem } from "./model"
import { addMembers, muteMember, removeMember, setRole, transferOwner, useMembers } from "./queries"

// 一次最多邀请 100 人（AddGroupMembersRequest 的 max_items）
const INVITE_MAX = 100

const MUTE_OPTIONS = [
  { label: "10 分钟", seconds: 600 },
  { label: "1 小时", seconds: 3600 },
  { label: "12 小时", seconds: 43_200 },
  { label: "1 天", seconds: 86_400 },
  { label: "7 天", seconds: 604_800 },
  { label: "30 天", seconds: 2_592_000 },
]

// 移除、禁言时对方已经不在群里，或者自己的身份刚被改掉
const MEMBER_ERRORS = { NOT_GROUP_MEMBER: "你已不在这个群里" }

type Pending = { kind: "remove" | "transfer" | "mute"; member: MemberItem } | null

// 群面板的「成员」页：角色徽章、禁言剩余时间（设计 3.6.7），按权限显示管理操作
export function GroupMembersTab({ group, conversation }: { group: GroupInfo; conversation?: Conversation }) {
  const members = useMembers(group.id, !group.dismissed && group.myRole !== "none")
  const meId = useSession((s) => s.userId) ?? ""
  const now = useNow(30_000)
  const [inviting, setInviting] = useState(false)
  const [pending, setPending] = useState<Pending>(null)
  const sorted = useMemo(() => sortMembers(members.data ?? []), [members.data])
  const manager = isManager(group.myRole) && !group.dismissed

  const run = async (action: () => Promise<unknown>, done: string) => {
    try {
      await action()
      toast(done)
    } catch (e) {
      toast(errorText(e, MEMBER_ERRORS))
    }
  }

  const actionsFor = (m: MemberItem): ActionItem[] => {
    if (m.user.id === meId || group.dismissed) return []
    const items: ActionItem[] = []
    const name = m.user.nickname
    if (group.myRole === "owner" && m.role !== "owner") {
      items.push(
        m.role === "admin"
          ? { label: "取消管理员", icon: ShieldOff, onSelect: () => void run(() => setRole(group.id, m.user.id, "member"), `已取消 ${name} 的管理员`) }
          : { label: "设为管理员", icon: ShieldCheck, onSelect: () => void run(() => setRole(group.id, m.user.id, "admin"), `已将 ${name} 设为管理员`) },
      )
    }
    if (canManage(group.myRole, m.role)) {
      const muted = m.mutedUntil !== undefined && m.mutedUntil > now
      items.push(
        muted
          ? { label: "解除禁言", icon: Volume2, onSelect: () => void run(() => muteMember(group.id, m.user.id, 0), `已解除 ${name} 的禁言`) }
          : { label: "禁言…", icon: MicOff, onSelect: () => setPending({ kind: "mute", member: m }) },
        { label: "移出群聊", icon: UserMinus, destructive: true, onSelect: () => setPending({ kind: "remove", member: m }) },
      )
    }
    if (group.myRole === "owner" && m.role !== "owner") {
      items.push({ label: "转让群主", icon: Crown, destructive: true, onSelect: () => setPending({ kind: "transfer", member: m }) })
    }
    return items
  }

  if (group.dismissed) return <p className="py-12 text-center text-sm text-muted-foreground">群已解散</p>

  return (
    <div className="flex flex-col gap-3">
      {manager && (
        <Button variant="outline" onClick={() => setInviting(true)}>
          <UserPlus />
          邀请好友
        </Button>
      )}
      <ListState query={members}>
        <ul className="space-y-0.5">
          <AnimatePresence initial={false}>
            {sorted.map((m) => {
              const muted = m.mutedUntil !== undefined && m.mutedUntil > now
              return (
                <motion.li key={m.user.id} layout exit={{ opacity: 0, x: -24 }} transition={springs.smooth}>
                  <div className="flex items-center gap-2.5 rounded-xl px-2 py-2 transition-colors hover:bg-muted/60">
                    <ProfileCard userId={m.user.id} user={m.user} conversation={conversation}>
                      <UserAvatar seed={m.user.id} name={m.user.nickname} avatarId={m.user.avatarId} size="sm" />
                    </ProfileCard>
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-1.5">
                        <span className="truncate text-sm">{m.user.nickname}</span>
                        {m.user.id === meId && <span className="shrink-0 text-xs text-muted-foreground">（我）</span>}
                        {(m.role === "owner" || m.role === "admin") && <RoleBadge role={m.role} />}
                      </div>
                      {muted && m.mutedUntil !== undefined && (
                        <p className="flex items-center gap-1 text-xs text-destructive">
                          <MicOff className="size-3" />
                          禁言中，剩 {formatRemaining(m.mutedUntil - now)}
                        </p>
                      )}
                    </div>
                    <ActionMenu label={`${m.user.nickname} 的管理操作`} items={actionsFor(m)} />
                  </div>
                </motion.li>
              )
            })}
          </AnimatePresence>
        </ul>
      </ListState>

      <InviteDialog group={group} open={inviting} onOpenChange={setInviting} memberIds={sorted.map((m) => m.user.id)} />
      <MuteDialog
        member={pending?.kind === "mute" ? pending.member : undefined}
        onClose={() => setPending(null)}
        onPick={(seconds, label) => {
          const m = pending?.member
          if (m) void run(() => muteMember(group.id, m.user.id, seconds), `已禁言 ${m.user.nickname} ${label}`)
          setPending(null)
        }}
      />
      <ConfirmDialog
        open={pending?.kind === "remove"}
        onOpenChange={(open) => !open && setPending(null)}
        title={`把 ${pending?.member.user.nickname ?? ""} 移出群聊`}
        description="移出后对方看不到这个群和之后的消息，会收到被移出的通知。"
        confirmLabel="移出"
        destructive
        errorOverrides={MEMBER_ERRORS}
        onConfirm={async () => {
          if (!pending) return
          await removeMember(group.id, pending.member.user.id)
          toast(`已将 ${pending.member.user.nickname} 移出群聊`)
        }}
      />
      <ConfirmDialog
        open={pending?.kind === "transfer"}
        onOpenChange={(open) => !open && setPending(null)}
        title={`把群主转让给 ${pending?.member.user.nickname ?? ""}`}
        description="转让后你会成为管理员，不能再解散群或任免管理员；只有新群主转回才能撤销。"
        confirmLabel="转让"
        destructive
        errorOverrides={MEMBER_ERRORS}
        onConfirm={async () => {
          if (!pending) return
          await transferOwner(group.id, pending.member.user.id)
          toast(`已把群主转让给 ${pending.member.user.nickname}`)
        }}
      />
    </div>
  )
}

function MuteDialog({
  member,
  onClose,
  onPick,
}: {
  member?: MemberItem
  onClose: () => void
  onPick: (seconds: number, label: string) => void
}) {
  return (
    <AppDialog open={member !== undefined} onOpenChange={(open) => !open && onClose()} title={`禁言 ${member?.user.nickname ?? ""}`} description="禁言期间对方不能在群里发言，到期自动解除。">
      <div className="grid grid-cols-3 gap-2 px-5 pb-5">
        {MUTE_OPTIONS.map((o) => (
          <Button key={o.seconds} variant="outline" onClick={() => onPick(o.seconds, o.label)}>
            {o.label}
          </Button>
        ))}
      </div>
    </AppDialog>
  )
}

function InviteDialog({
  group,
  open,
  onOpenChange,
  memberIds,
}: {
  group: GroupInfo
  open: boolean
  onOpenChange: (open: boolean) => void
  memberIds: string[]
}) {
  return (
    <AppDialog open={open} onOpenChange={onOpenChange} title="邀请好友入群" description={`被邀请的好友会直接加入群「${group.name}」。`}>
      {open && <InviteBody group={group} memberIds={memberIds} onDone={() => onOpenChange(false)} />}
    </AppDialog>
  )
}

function InviteBody({ group, memberIds, onDone }: { group: GroupInfo; memberIds: string[]; onDone: () => void }) {
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [sending, setSending] = useState(false)
  const [error, setError] = useState<string>()
  const existing = useMemo(() => new Set(memberIds), [memberIds])

  const toggle = (id: string) =>
    setSelected((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })

  const submit = async () => {
    setSending(true)
    setError(undefined)
    try {
      const added = await addMembers(group.id, [...selected])
      toast(added.length > 0 ? `已邀请 ${added.length} 位好友入群` : "选中的好友都已经在群里了")
      onDone()
    } catch (e) {
      setError(errorText(e, { NOT_FRIENDS: "只能邀请好友入群，有人已不是你的好友" }))
      setSending(false)
    }
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-3 px-5 pb-5">
      <div className="flex h-80 min-h-0 flex-col">
        <FriendPicker selected={selected} onToggle={toggle} disabledIds={existing} max={INVITE_MAX} />
      </div>
      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      )}
      <Button disabled={selected.size === 0 || sending} onClick={() => void submit()} className={cn(selected.size === 0 && "opacity-60")}>
        {sending ? "邀请中…" : selected.size > 0 ? `邀请 ${selected.size} 人` : "邀请"}
      </Button>
    </div>
  )
}
