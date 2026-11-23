import { useState } from "react"
import { LogOut, Pencil, Trash2 } from "lucide-react"
import { toast } from "sonner"
import { errorText, isApiError } from "@/api/errors"
import { AvatarUpload } from "@/components/common/AvatarUpload"
import { ConfirmDialog } from "@/components/common/ConfirmDialog"
import { CopyId } from "@/components/common/CopyId"
import { UserAvatar } from "@/components/common/UserAvatar"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"
import { cn, codePoints } from "@/lib/utils"
import { chat } from "@/realtime/client"
import { isManager, type GroupInfo as Group, type JoinPolicy } from "./model"
import { dismissGroup, leaveGroup, updateGroup, useGroup } from "./queries"

const NAME_MAX = 30
const NOTICE_MAX = 500

// 群面板的「资料」页：名称、群 ID、公告、加群方式、全员禁言；群主和管理员可以修改，底部是退群和解散
export function GroupInfoTab({ group }: { group: Group }) {
  const [editing, setEditing] = useState(false)
  const manager = isManager(group.myRole) && !group.dismissed

  return (
    <div className="flex flex-col gap-5">
      <div className="flex flex-col items-center gap-2 pt-2 text-center">
        {/* 编辑表单以打开时的版本号为基准（第 47 条），编辑期间不能换头像，否则保存表单会被当成版本冲突 */}
        {manager && !editing ? (
          <AvatarUpload
            compact
            seed={group.id}
            name={group.name}
            avatarId={group.avatarId}
            shape="square"
            label="群头像"
            onChange={async (avatar) => void (await updateGroup(group.id, group.version, { avatar }))}
          />
        ) : (
          <UserAvatar seed={group.id} name={group.name} avatarId={group.avatarId} shape="square" size="xl" />
        )}
        <p className="font-display text-lg font-semibold break-all">{group.name}</p>
        <CopyId id={group.id} label="群 ID" />
        <p className="text-xs text-muted-foreground">
          {group.dismissed ? "已解散" : `${group.memberCount} 位成员 · ${group.joinMode === "approval" ? "加群需要审核" : "可以直接加入"}`}
        </p>
      </div>

      {editing ? (
        <EditForm group={group} onDone={() => setEditing(false)} />
      ) : (
        <section className="rounded-xl bg-muted/50 p-3.5">
          <div className="flex items-center justify-between">
            <h3 className="text-sm font-medium">群公告</h3>
            {manager && (
              <Button variant="ghost" size="xs" onClick={() => setEditing(true)}>
                <Pencil />
                编辑资料
              </Button>
            )}
          </div>
          <p className={cn("mt-1.5 text-sm break-words whitespace-pre-wrap", !group.notice && "text-muted-foreground")}>
            {group.notice || "暂无公告"}
          </p>
        </section>
      )}

      {manager && <MuteAllSwitch group={group} />}

      {!group.dismissed && group.myRole !== "none" && <DangerZone group={group} />}
    </div>
  )
}

function EditForm({ group, onDone }: { group: Group; onDone: () => void }) {
  const fresh = useGroup(group.id)
  // 打开表单时的资料。group 会随通知刷新，不能拿它当基准：那样版本号总是最新的，
  // 不会触发版本冲突，而且别人刚改的字段会被表单里的旧值当成「修改」覆盖回去
  const [base, setBase] = useState(group)
  const [name, setName] = useState(group.name)
  const [notice, setNotice] = useState(group.notice)
  const [joinMode, setJoinMode] = useState<JoinPolicy>(group.joinMode)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string>()
  const nameLength = codePoints(name.trim())
  const valid = nameLength >= 1 && nameLength <= NAME_MAX && codePoints(notice) <= NOTICE_MAX

  const save = async () => {
    // 只发送变化的字段，避免覆盖别人同时改的其他字段
    const patch = {
      name: name.trim() !== base.name ? name.trim() : undefined,
      notice: notice.trim() !== base.notice ? notice.trim() : undefined,
      joinMode: joinMode !== base.joinMode ? joinMode : undefined,
    }
    if (Object.values(patch).every((v) => v === undefined)) {
      onDone()
      return
    }
    setSaving(true)
    setError(undefined)
    try {
      await updateGroup(base.id, base.version, patch)
      toast("群资料已保存")
      onDone()
    } catch (e) {
      setError(errorText(e))
      if (isApiError(e) && e.reason === "GROUP_VERSION_CONFLICT") {
        // 已显示最新内容（errors.ts 的文案）：用重新读到的资料重置表单
        const latest = (await fresh.refetch()).data
        if (latest) {
          setBase(latest)
          setName(latest.name)
          setNotice(latest.notice)
          setJoinMode(latest.joinMode)
        }
      }
      setSaving(false)
    }
  }

  return (
    <form
      className="flex flex-col gap-3 rounded-xl border p-3.5"
      onSubmit={(e) => {
        e.preventDefault()
        if (valid && !saving) void save()
      }}
    >
      <div className="flex flex-col gap-1.5">
        <Label htmlFor="edit-group-name">群名称</Label>
        <Input id="edit-group-name" value={name} onChange={(e) => setName(e.target.value)} aria-invalid={nameLength > NAME_MAX || nameLength === 0} />
      </div>
      <div className="flex flex-col gap-1.5">
        <Label htmlFor="edit-group-notice">群公告</Label>
        <Textarea id="edit-group-notice" value={notice} onChange={(e) => setNotice(e.target.value)} className="min-h-24" />
        <p className={cn("text-right text-xs", codePoints(notice) > NOTICE_MAX ? "text-destructive" : "text-muted-foreground")}>
          {codePoints(notice)}/{NOTICE_MAX}
        </p>
      </div>
      <div className="flex gap-2">
        {(["free", "approval"] as const).map((mode) => (
          <button
            key={mode}
            type="button"
            aria-pressed={joinMode === mode}
            onClick={() => setJoinMode(mode)}
            className={cn(
              "flex-1 rounded-lg border px-2 py-1.5 text-sm transition-colors outline-none focus-visible:ring-2 focus-visible:ring-ring",
              joinMode === mode ? "border-brand-strong/60 bg-brand/5 font-medium" : "text-muted-foreground hover:bg-muted/60",
            )}
          >
            {mode === "free" ? "直接加入" : "需要审核"}
          </button>
        ))}
      </div>
      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      )}
      <div className="flex justify-end gap-2">
        <Button type="button" variant="ghost" size="sm" onClick={onDone} disabled={saving}>
          取消
        </Button>
        <Button type="submit" size="sm" disabled={!valid || saving}>
          {saving ? "保存中…" : "保存"}
        </Button>
      </div>
    </form>
  )
}

function MuteAllSwitch({ group }: { group: Group }) {
  const [pending, setPending] = useState(false)
  const toggle = async () => {
    setPending(true)
    try {
      await updateGroup(group.id, group.version, { muteAll: !group.muteAll })
      toast(group.muteAll ? "已关闭全员禁言" : "已开启全员禁言，只有群主和管理员可以发言")
    } catch (e) {
      toast(errorText(e))
    } finally {
      setPending(false)
    }
  }
  return (
    <div className="flex items-center justify-between gap-3 rounded-xl border px-3.5 py-3">
      <div>
        <p className="text-sm font-medium">全员禁言</p>
        <p className="text-xs text-muted-foreground">开启后只有群主和管理员可以发言</p>
      </div>
      <button
        type="button"
        role="switch"
        aria-checked={group.muteAll}
        aria-label="全员禁言"
        disabled={pending}
        onClick={() => void toggle()}
        className={cn(
          "relative h-6 w-10 shrink-0 rounded-full transition-colors outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-60",
          group.muteAll ? "bg-brand-strong" : "bg-input dark:bg-muted",
        )}
      >
        <span
          className={cn(
            "absolute top-0.5 left-0.5 size-5 rounded-full bg-white shadow-sm transition-transform",
            group.muteAll && "translate-x-4",
          )}
        />
      </button>
    </div>
  )
}

function DangerZone({ group }: { group: Group }) {
  const [confirm, setConfirm] = useState<"leave" | "dismiss" | null>(null)
  const owner = group.myRole === "owner"

  // 退群的会话随后也会由通知移除，这里先移掉（同时回到会话列表），界面立即生效
  const leave = async () => {
    await leaveGroup(group.id)
    chat.removeConversation(group.id)
    toast(`已退出群「${group.name}」`)
  }
  const dismiss = async () => {
    await dismissGroup(group.id)
    toast(`群「${group.name}」已解散`)
  }

  return (
    <section className="flex flex-col gap-2 border-t pt-4">
      {owner ? (
        <>
          <Button variant="destructive" onClick={() => setConfirm("dismiss")}>
            <Trash2 />
            解散群聊
          </Button>
          <p className="text-center text-xs text-muted-foreground">群主要退群，先在成员列表里把群主转让给其他人</p>
        </>
      ) : (
        <Button variant="destructive" onClick={() => setConfirm("leave")}>
          <LogOut />
          退出群聊
        </Button>
      )}
      <ConfirmDialog
        open={confirm === "leave"}
        onOpenChange={(open) => !open && setConfirm(null)}
        title={`退出群「${group.name}」`}
        description="退出后这个群会从会话列表里消失，也看不到之后的消息；可以按群 ID 重新申请加入。"
        confirmLabel="退出"
        destructive
        onConfirm={leave}
      />
      <ConfirmDialog
        open={confirm === "dismiss"}
        onOpenChange={(open) => !open && setConfirm(null)}
        title={`解散群「${group.name}」`}
        description="解散后所有成员都不能再发言，只能查看历史消息，这个操作不能撤销。请输入群名称确认。"
        confirmLabel="解散"
        destructive
        confirmText={group.name}
        onConfirm={dismiss}
      />
    </section>
  )
}
