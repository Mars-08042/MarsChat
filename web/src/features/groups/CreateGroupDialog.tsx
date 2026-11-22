import { useState } from "react"
import { AnimatePresence, motion } from "motion/react"
import { useNavigate } from "react-router"
import { Check, PartyPopper } from "lucide-react"
import { errorText } from "@/api/errors"
import { AppDialog } from "@/components/common/AppDialog"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"
import { springs } from "@/lib/motion"
import { cn, codePoints } from "@/lib/utils"
import { useUi } from "@/stores/ui"
import { FriendPicker } from "./FriendPicker"
import type { GroupInfo, JoinPolicy } from "./model"
import { createGroup } from "./queries"

const NAME_MAX = 30
const NOTICE_MAX = 500
// social 的 max_initial_members（默认 50，不含群主）；超出时服务端返回 VALIDATOR
const INITIAL_MEMBERS_MAX = 50

const STEPS = ["群资料", "邀请好友", "完成"] as const

// 建群向导：名称、加群方式 → 邀请好友 → 完成（设计 3.6.7）。群头像在第 7 阶段（文件）接入
export function CreateGroupDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  return (
    <AppDialog open={open} onOpenChange={onOpenChange} title="创建群聊" className="w-[min(28rem,calc(100vw-2rem))]">
      {open && <Wizard onClose={() => onOpenChange(false)} />}
    </AppDialog>
  )
}

function Wizard({ onClose }: { onClose: () => void }) {
  const navigate = useNavigate()
  const celebrate = useUi((s) => s.celebrate)
  const [step, setStep] = useState(0)
  const [name, setName] = useState("")
  const [notice, setNotice] = useState("")
  const [joinMode, setJoinMode] = useState<JoinPolicy>("free")
  const [members, setMembers] = useState<Set<string>>(new Set())
  const [creating, setCreating] = useState(false)
  const [error, setError] = useState<string>()
  const [created, setCreated] = useState<GroupInfo>()

  const nameLength = codePoints(name.trim())
  const nameValid = nameLength >= 1 && nameLength <= NAME_MAX
  const noticeValid = codePoints(notice) <= NOTICE_MAX

  const toggle = (id: string) =>
    setMembers((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })

  const submit = async () => {
    setCreating(true)
    setError(undefined)
    try {
      const group = await createGroup({ name: name.trim(), notice: notice.trim(), joinMode, memberIds: [...members] })
      setCreated(group)
      setStep(2)
      celebrate()
    } catch (e) {
      setError(errorText(e, { NOT_FRIENDS: "只能邀请好友入群，有人已不是你的好友" }))
    } finally {
      setCreating(false)
    }
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col px-5 pb-5">
      <Stepper step={step} />
      <AnimatePresence mode="wait" initial={false}>
        <motion.div
          key={step}
          initial={{ opacity: 0, x: 16 }}
          animate={{ opacity: 1, x: 0 }}
          exit={{ opacity: 0, x: -16 }}
          transition={springs.snappy}
          className="flex min-h-0 flex-1 flex-col pt-4"
        >
          {step === 0 && (
            <form
              className="flex flex-col gap-4"
              onSubmit={(e) => {
                e.preventDefault()
                if (nameValid && noticeValid) setStep(1)
              }}
            >
              <div className="flex flex-col gap-1.5">
                <Label htmlFor="group-name">群名称</Label>
                <Input id="group-name" autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="例如 周末登山小队" />
                <p className={cn("text-right text-xs", nameLength > NAME_MAX ? "text-destructive" : "text-muted-foreground")}>
                  {nameLength}/{NAME_MAX}
                </p>
              </div>
              <div className="flex flex-col gap-1.5">
                <Label htmlFor="group-notice">群公告（选填）</Label>
                <Textarea id="group-notice" value={notice} onChange={(e) => setNotice(e.target.value)} placeholder="成员都能看到" />
                {!noticeValid && <p className="text-xs text-destructive">公告最多 {NOTICE_MAX} 字</p>}
              </div>
              <fieldset className="flex flex-col gap-2">
                <legend className="mb-1.5 text-sm font-medium">加群方式</legend>
                <JoinModeOption value="free" current={joinMode} onPick={setJoinMode} title="直接加入" hint="知道群 ID 的人可以直接进群" />
                <JoinModeOption value="approval" current={joinMode} onPick={setJoinMode} title="需要审核" hint="群主或管理员同意后才能进群" />
              </fieldset>
              <Button type="submit" disabled={!nameValid || !noticeValid}>
                下一步
              </Button>
            </form>
          )}

          {step === 1 && (
            <div className="flex min-h-0 flex-1 flex-col gap-3">
              <p className="text-sm text-muted-foreground">选择要拉进群的好友，也可以以后再邀请。</p>
              <div className="flex h-72 min-h-0 flex-col">
                <FriendPicker selected={members} onToggle={toggle} max={INITIAL_MEMBERS_MAX} />
              </div>
              {error && (
                <p role="alert" className="text-sm text-destructive">
                  {error}
                </p>
              )}
              <div className="flex justify-between gap-2">
                <Button variant="ghost" onClick={() => setStep(0)} disabled={creating}>
                  上一步
                </Button>
                <Button onClick={() => void submit()} disabled={creating}>
                  {creating ? "创建中…" : members.size > 0 ? `创建（${members.size + 1} 人）` : "创建"}
                </Button>
              </div>
            </div>
          )}

          {step === 2 && created && (
            <div className="flex flex-col items-center gap-3 py-4 text-center">
              <motion.span
                initial={{ scale: 0.5, rotate: -20 }}
                animate={{ scale: 1, rotate: 0 }}
                transition={springs.snappy}
                className="flex size-14 items-center justify-center rounded-full bg-brand/15 text-brand"
              >
                <PartyPopper className="size-7" />
              </motion.span>
              <p className="font-display text-lg font-semibold">群「{created.name}」创建好了</p>
              <p className="text-sm text-muted-foreground">
                共 {created.memberCount} 人。其他人可以用群 ID <span className="font-mono">{created.id}</span> 申请加入。
              </p>
              <div className="mt-2 flex w-full gap-2">
                <Button variant="outline" className="flex-1" onClick={onClose}>
                  完成
                </Button>
                <Button
                  className="flex-1"
                  onClick={() => {
                    onClose()
                    void navigate(`/chat/${created.id}`)
                  }}
                >
                  进入群聊
                </Button>
              </div>
            </div>
          )}
        </motion.div>
      </AnimatePresence>
    </div>
  )
}

function Stepper({ step }: { step: number }) {
  return (
    <ol className="flex items-center gap-2" aria-label="建群步骤">
      {STEPS.map((label, i) => {
        const done = i < step
        const active = i === step
        return (
          <li key={label} className="flex flex-1 items-center gap-2" aria-current={active ? "step" : undefined}>
            <span
              className={cn(
                "flex size-6 shrink-0 items-center justify-center rounded-full text-xs font-semibold transition-colors",
                done || active ? "bg-brand-strong text-white" : "bg-muted text-muted-foreground",
              )}
            >
              {done ? <Check className="size-3.5" strokeWidth={3} /> : i + 1}
            </span>
            <span className={cn("text-xs whitespace-nowrap", active ? "font-medium text-foreground" : "text-muted-foreground")}>{label}</span>
            {i < STEPS.length - 1 && (
              <span className="relative h-px flex-1 overflow-hidden bg-border">
                <motion.span
                  className="absolute inset-0 origin-left bg-brand-strong"
                  initial={false}
                  animate={{ scaleX: done ? 1 : 0 }}
                  transition={springs.smooth}
                />
              </span>
            )}
          </li>
        )
      })}
    </ol>
  )
}

function JoinModeOption({
  value,
  current,
  onPick,
  title,
  hint,
}: {
  value: JoinPolicy
  current: JoinPolicy
  onPick: (v: JoinPolicy) => void
  title: string
  hint: string
}) {
  const checked = value === current
  return (
    <label
      className={cn(
        "flex cursor-pointer items-start gap-3 rounded-lg border px-3 py-2.5 transition-colors has-focus-visible:ring-2 has-focus-visible:ring-ring",
        checked ? "border-brand-strong/60 bg-brand/5" : "hover:bg-muted/60",
      )}
    >
      <input type="radio" name="join-mode" value={value} checked={checked} onChange={() => onPick(value)} className="mt-1 accent-[var(--brand-strong)]" />
      <span>
        <span className="block text-sm font-medium">{title}</span>
        <span className="block text-xs text-muted-foreground">{hint}</span>
      </span>
    </label>
  )
}
