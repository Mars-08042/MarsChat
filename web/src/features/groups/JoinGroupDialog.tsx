import { useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { Link, useNavigate } from "react-router"
import { MessageCircle, Search } from "lucide-react"
import { toast } from "sonner"
import { errorText, isApiError } from "@/api/errors"
import { AppDialog } from "@/components/common/AppDialog"
import { UserAvatar } from "@/components/common/UserAvatar"
import { Button, buttonVariants } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { cn } from "@/lib/utils"
import { groupQuery, joinGroup } from "./queries"

const GROUP_ID = /^\d{1,19}$/
const MESSAGE_MAX = 100

// 按群 ID 加入：先查出群资料确认（非成员也能查，只是看不到公告），直接加入的群立即进群，需审核的群提交申请
export function JoinGroupDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  return (
    <AppDialog open={open} onOpenChange={onOpenChange} title="加入群聊" description="输入群 ID，群成员可以在群资料里复制。">
      {open && <JoinBody onDone={() => onOpenChange(false)} />}
    </AppDialog>
  )
}

function JoinBody({ onDone }: { onDone: () => void }) {
  const [input, setInput] = useState("")
  const [lookup, setLookup] = useState<string>()
  const trimmed = input.trim()
  const valid = GROUP_ID.test(trimmed)

  return (
    <div className="flex flex-col gap-4 overflow-y-auto px-5 pb-5">
      <form
        className="flex flex-col gap-1.5"
        onSubmit={(e) => {
          e.preventDefault()
          if (valid) setLookup(trimmed)
        }}
      >
        <Label htmlFor="group-id">群 ID</Label>
        <div className="flex gap-2">
          <Input
            id="group-id"
            autoFocus
            inputMode="numeric"
            autoComplete="off"
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
        {trimmed !== "" && !valid && <p className="text-xs text-destructive">群 ID 只包含数字</p>}
      </form>
      {lookup && <GroupCandidate key={lookup} groupId={lookup} onDone={onDone} />}
    </div>
  )
}

function GroupCandidate({ groupId, onDone }: { groupId: string; onDone: () => void }) {
  const navigate = useNavigate()
  const group = useQuery({ ...groupQuery(groupId), staleTime: 0 })
  const [message, setMessage] = useState("")
  const [joining, setJoining] = useState(false)

  if (group.isPending) return <p className="py-4 text-center text-sm text-muted-foreground">正在查找…</p>
  if (group.isError) {
    const notFound = isApiError(group.error) && group.error.reason === "GROUP_NOT_FOUND"
    return <p className="rounded-lg bg-muted px-3 py-2 text-sm text-muted-foreground">{notFound ? "没有找到这个群，请检查 ID" : errorText(group.error)}</p>
  }
  const g = group.data
  const approval = g.joinMode === "approval"

  const join = async () => {
    setJoining(true)
    try {
      const { joined } = await joinGroup(groupId, approval ? message.trim() : "")
      if (joined) {
        toast(`你已加入群「${g.name}」`)
        onDone()
        void navigate(`/chat/${groupId}`)
      } else {
        toast("已发送入群申请", { description: "群主或管理员同意后会通知你" })
        onDone()
      }
    } catch (e) {
      toast(errorText(e))
      setJoining(false)
    }
  }

  return (
    <div className="flex flex-col gap-3 rounded-xl border bg-card p-4">
      <div className="flex items-center gap-3">
        <UserAvatar seed={g.id} name={g.name} avatarId={g.avatarId} shape="square" size="lg" />
        <div className="min-w-0">
          <p className="truncate font-semibold">{g.name}</p>
          <p className="text-sm text-muted-foreground">
            {g.memberCount} 位成员 · {approval ? "需要审核" : "直接加入"}
          </p>
        </div>
      </div>
      {g.dismissed ? (
        <p className="rounded-lg bg-muted px-3 py-2 text-center text-sm text-muted-foreground">这个群已解散</p>
      ) : g.myRole !== "none" ? (
        <Link to={`/chat/${groupId}`} onClick={onDone} className={cn(buttonVariants({ size: "sm" }), "w-full")}>
          <MessageCircle />
          你已经在群里，进入群聊
        </Link>
      ) : (
        <form
          className="flex flex-col gap-2"
          onSubmit={(e) => {
            e.preventDefault()
            void join()
          }}
        >
          {approval && (
            <>
              <Label htmlFor="join-message">申请附言</Label>
              <Input
                id="join-message"
                value={message}
                maxLength={MESSAGE_MAX}
                placeholder="选填，最多 100 字"
                onChange={(e) => setMessage(e.target.value)}
              />
            </>
          )}
          <Button type="submit" disabled={joining}>
            {joining ? "处理中…" : approval ? "申请加入" : "加入群聊"}
          </Button>
        </form>
      )}
    </div>
  )
}
