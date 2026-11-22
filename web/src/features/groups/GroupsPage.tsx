import { useState } from "react"
import { AnimatePresence, motion } from "motion/react"
import { Link } from "react-router"
import { LogIn, Plus } from "lucide-react"
import { ListState } from "@/components/common/ListState"
import { UserAvatar } from "@/components/common/UserAvatar"
import { Button } from "@/components/ui/button"
import { springs } from "@/lib/motion"
import { CreateGroupDialog } from "./CreateGroupDialog"
import { JoinGroupDialog } from "./JoinGroupDialog"
import type { GroupInfo } from "./model"
import { useGroups } from "./queries"
import { RoleBadge } from "./RoleBadge"

// 我的群：列表、建群向导、按 ID 加群（设计 3.5：建群用对话框，不单独占路由）。点一个群进入群聊
export function GroupsPage() {
  const groups = useGroups()
  const [creating, setCreating] = useState(false)
  const [joining, setJoining] = useState(false)
  const list = groups.data ?? []

  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-y-auto [scrollbar-width:thin]">
      <div className="mx-auto w-full max-w-2xl px-4 pt-6 pb-10 md:px-8 md:pt-10">
        <header className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <h1 className="font-display text-2xl font-semibold">群组</h1>
            {list.length > 0 && <p className="mt-1 text-sm text-muted-foreground">已加入 {list.length} 个群</p>}
          </div>
          <div className="flex gap-2">
            <Button variant="outline" onClick={() => setJoining(true)}>
              <LogIn />
              加入群
            </Button>
            <Button onClick={() => setCreating(true)}>
              <Plus />
              创建群
            </Button>
          </div>
        </header>

        <div className="mt-6">
          <ListState
            query={groups}
            empty={
              list.length === 0 ? (
                <div className="flex flex-col items-center gap-3">
                  <p>还没有加入任何群</p>
                  <div className="flex gap-2">
                    <Button variant="outline" size="sm" onClick={() => setJoining(true)}>
                      按 ID 加入
                    </Button>
                    <Button size="sm" onClick={() => setCreating(true)}>
                      创建一个群
                    </Button>
                  </div>
                </div>
              ) : undefined
            }
          >
            <ul className="space-y-0.5">
              <AnimatePresence initial={false}>
                {list.map((g) => (
                  <motion.li key={g.id} layout initial={{ opacity: 0, y: 6 }} animate={{ opacity: 1, y: 0 }} exit={{ opacity: 0 }} transition={springs.smooth}>
                    <GroupRow group={g} />
                  </motion.li>
                ))}
              </AnimatePresence>
            </ul>
          </ListState>
        </div>
      </div>
      <CreateGroupDialog open={creating} onOpenChange={setCreating} />
      <JoinGroupDialog open={joining} onOpenChange={setJoining} />
    </div>
  )
}

function GroupRow({ group: g }: { group: GroupInfo }) {
  return (
    <Link
      to={`/chat/${g.id}`}
      className="flex items-center gap-3 rounded-xl px-3 py-2.5 transition-colors outline-none hover:bg-muted/60 focus-visible:ring-2 focus-visible:ring-ring"
    >
      <UserAvatar seed={g.id} name={g.name} shape="square" />
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-1.5">
          <span className="truncate font-medium">{g.name}</span>
          {(g.myRole === "owner" || g.myRole === "admin") && <RoleBadge role={g.myRole} />}
        </div>
        <p className="truncate text-xs text-muted-foreground">
          {g.dismissed ? "已解散" : `${g.memberCount} 位成员`}
          {g.muteAll && !g.dismissed && " · 全员禁言"}
          {g.joinMode === "approval" && !g.dismissed && " · 需审核"}
        </p>
      </div>
    </Link>
  )
}
