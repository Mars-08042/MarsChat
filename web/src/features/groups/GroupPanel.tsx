import { useState } from "react"
import { AnimatePresence, motion } from "motion/react"
import { toast } from "sonner"
import { errorText } from "@/api/errors"
import { ListState } from "@/components/common/ListState"
import { UserAvatar } from "@/components/common/UserAvatar"
import { Button } from "@/components/ui/button"
import type { Conversation } from "@/features/chat/types"
import { ProfileCard } from "@/features/contacts/ProfileCard"
import { springs } from "@/lib/motion"
import { formatListTime } from "@/lib/time"
import { useNow } from "@/lib/use-countdown"
import { cn } from "@/lib/utils"
import { useUi, type PanelTab } from "@/stores/ui"
import { GroupInfoTab } from "./GroupInfo"
import { GroupMembersTab } from "./GroupMembers"
import { isManager, type GroupInfo, type JoinRequestItem } from "./model"
import { acceptJoinRequest, rejectJoinRequest, useGroup, useJoinRequests } from "./queries"

// 会话右侧的群面板：资料、成员、入群申请（只有群主和管理员看得到）。群聊会话 ID 即群 ID
export function GroupPanel({ conversation }: { conversation: Conversation }) {
  const group = useGroup(conversation.id)
  return (
    <ListState query={group}>
      {group.data && <GroupTabs group={group.data} conversation={conversation} />}
    </ListState>
  )
}

function GroupTabs({ group, conversation }: { group: GroupInfo; conversation: Conversation }) {
  const manager = isManager(group.myRole) && !group.dismissed
  const requests = useJoinRequests(group.id, manager)
  const panel = useUi((s) => s.panel)
  const openPanel = useUi((s) => s.openPanel)
  const pendingCount = manager ? (requests.data?.length ?? 0) : 0
  // 没有权限时「申请」页不存在，回到资料页
  const tab: PanelTab = panel?.tab === "requests" && !manager ? "info" : (panel?.tab ?? "info")

  const tabs: { tab: PanelTab; label: string; count?: number }[] = [
    { tab: "info", label: "资料" },
    { tab: "members", label: "成员", count: group.dismissed ? undefined : group.memberCount },
    ...(manager ? [{ tab: "requests" as const, label: "申请", count: pendingCount }] : []),
  ]

  return (
    <div className="flex flex-col gap-4">
      <div role="tablist" aria-label="群资料" className="flex gap-1 rounded-xl bg-muted p-1">
        {tabs.map((t) => (
          <button
            key={t.tab}
            type="button"
            role="tab"
            aria-selected={tab === t.tab}
            onClick={() => openPanel(conversation.id, t.tab)}
            className={cn(
              "relative flex-1 rounded-lg px-2 py-1.5 text-sm font-medium transition-colors outline-none focus-visible:ring-2 focus-visible:ring-ring",
              tab === t.tab ? "text-foreground" : "text-muted-foreground hover:text-foreground",
            )}
          >
            {tab === t.tab && <motion.span layoutId="group-panel-tab" transition={springs.smooth} className="absolute inset-0 rounded-lg bg-background shadow-sm" />}
            <span className="relative inline-flex items-center gap-1">
              {t.label}
              {t.tab === "requests" && pendingCount > 0 ? (
                <span className="min-w-4 rounded-full bg-gold px-1 text-center font-mono text-[10px] leading-4 font-semibold text-gold-foreground">
                  {pendingCount}
                </span>
              ) : (
                t.count !== undefined && t.tab !== "requests" && <span className="font-mono text-xs text-muted-foreground">{t.count}</span>
              )}
            </span>
          </button>
        ))}
      </div>
      {tab === "info" && <GroupInfoTab group={group} />}
      {tab === "members" && <GroupMembersTab group={group} conversation={conversation} />}
      {tab === "requests" && <RequestsTab groupId={group.id} />}
    </div>
  )
}

function RequestsTab({ groupId }: { groupId: string }) {
  const requests = useJoinRequests(groupId)
  return (
    <ListState query={requests} empty={requests.data?.length === 0 ? "没有待处理的入群申请" : undefined}>
      <ul className="space-y-2">
        <AnimatePresence initial={false}>
          {requests.data?.map((r) => (
            <motion.li
              key={r.id}
              layout
              initial={{ opacity: 0, y: 8 }}
              animate={{ opacity: 1, y: 0 }}
              exit={{ opacity: 0, x: 40, transition: { duration: 0.2 } }}
              transition={springs.smooth}
            >
              <JoinRequestCard request={r} />
            </motion.li>
          ))}
        </AnimatePresence>
      </ul>
    </ListState>
  )
}

function JoinRequestCard({ request: r }: { request: JoinRequestItem }) {
  const now = useNow()
  const [busy, setBusy] = useState<"accept" | "reject" | null>(null)

  const act = async (kind: "accept" | "reject") => {
    setBusy(kind)
    try {
      if (kind === "accept") {
        await acceptJoinRequest(r.groupId, r.id)
        toast(`已同意 ${r.user.nickname} 入群`)
      } else {
        await rejectJoinRequest(r.groupId, r.id)
      }
    } catch (e) {
      // 已被其他管理员处理时列表随后刷新，这条会自己消失
      toast(errorText(e, { REQUEST_ALREADY_HANDLED: "这条申请已被其他管理员处理" }))
      setBusy(null)
    }
  }

  return (
    <div className="flex items-start gap-3 rounded-xl border bg-card p-3">
      <ProfileCard userId={r.user.id} user={r.user}>
        <UserAvatar seed={r.user.id} name={r.user.nickname} src={r.user.avatarUrl} size="sm" />
      </ProfileCard>
      <div className="min-w-0 flex-1">
        <div className="flex items-baseline justify-between gap-2">
          <p className="truncate text-sm font-medium">{r.user.nickname}</p>
          <span className="shrink-0 text-xs text-muted-foreground">{formatListTime(r.createdAt, now)}</span>
        </div>
        <p className="mt-0.5 text-sm break-words text-muted-foreground">{r.message || "申请加入群聊"}</p>
        <div className="mt-2.5 flex justify-end gap-2">
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
