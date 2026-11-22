import { useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { Ban, LoaderCircle, Search, ShieldCheck } from "lucide-react"
import { toast } from "sonner"
import { api } from "@/api/client"
import { banUser, getUser, unbanUser } from "@/api/endpoints"
import { errorText, isApiError } from "@/api/errors"
import { ConfirmDialog } from "@/components/common/ConfirmDialog"
import { CopyId } from "@/components/common/CopyId"
import { UserAvatar } from "@/components/common/UserAvatar"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Role, UserStatus, type UserProfile } from "@/gen/account/v1/account_pb"
import { queryClient } from "@/lib/query"
import { useSession } from "@/stores/session"

const ID_PATTERN = /^[1-9]\d{0,18}$/
const adminUserKey = (id: string) => ["admin", "user", id] as const

// 管理页（设计 3.5）：按用户 ID 封禁、解封。后端没有用户列表接口，只能按 ID 查。
// 导航栏只对系统管理员显示入口；直接打开地址时，非管理员看到没有权限的说明
export function AdminPage() {
  const role = useSession((s) => s.user?.role)
  const [input, setInput] = useState("")
  const [target, setTarget] = useState<string>()

  if (role === undefined) {
    return (
      <div className="flex flex-1 items-center justify-center text-muted-foreground">
        <LoaderCircle aria-label="加载中" className="animate-spin motion-reduce:animate-none" />
      </div>
    )
  }
  if (role !== Role.ADMIN) {
    return (
      <div className="flex flex-1 flex-col items-center justify-center gap-2 p-6 text-center">
        <ShieldCheck className="size-8 text-muted-foreground" />
        <p className="font-medium">没有权限访问管理页</p>
        <p className="text-sm text-muted-foreground">只有系统管理员可以封禁和解封用户。</p>
      </div>
    )
  }

  const id = input.trim()
  const valid = ID_PATTERN.test(id)

  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-y-auto [scrollbar-width:thin]">
      <div className="mx-auto w-full max-w-2xl px-4 pt-6 pb-10 md:px-8 md:pt-10">
        <h1 className="font-display text-2xl font-semibold">管理</h1>
        <p className="mt-1 text-sm text-muted-foreground">按用户 ID 封禁或解封账号。封禁后对方的所有设备立即下线，之后无法登录，直到解封。</p>

        <form
          className="mt-6 flex gap-2"
          onSubmit={(e) => {
            e.preventDefault()
            if (valid) setTarget(id)
          }}
        >
          <label htmlFor="admin-user-id" className="sr-only">
            用户 ID
          </label>
          <Input id="admin-user-id" inputMode="numeric" placeholder="用户 ID" value={input} onChange={(e) => setInput(e.target.value)} className="font-mono" />
          <Button type="submit" variant="outline" disabled={!valid}>
            <Search />
            查找
          </Button>
        </form>

        {target && <TargetUser key={target} userId={target} />}
      </div>
    </div>
  )
}

function TargetUser({ userId }: { userId: string }) {
  const meId = useSession((s) => s.userId)
  const [confirming, setConfirming] = useState(false)
  const [unbanning, setUnbanning] = useState(false)
  // 每次查找都取最新状态：封禁可能刚被别的管理员改过
  const user = useQuery({
    queryKey: adminUserKey(userId),
    queryFn: async () => (await api.send(getUser, { userId: BigInt(userId) })).user,
    staleTime: 0,
  })
  const refresh = () => queryClient.invalidateQueries({ queryKey: adminUserKey(userId) })

  if (user.isPending) {
    return (
      <div className="flex justify-center py-10 text-muted-foreground">
        <LoaderCircle aria-label="加载中" className="animate-spin motion-reduce:animate-none" />
      </div>
    )
  }
  if (user.isError || !user.data) {
    const notFound = !user.data || (isApiError(user.error) && user.error.reason === "USER_NOT_FOUND")
    return <p className="mt-6 rounded-xl border border-dashed p-6 text-center text-sm text-muted-foreground">{notFound ? "没有这个用户" : errorText(user.error)}</p>
  }

  const u: UserProfile = user.data
  const banned = u.status === UserStatus.BANNED
  const self = userId === meId

  const unban = async () => {
    setUnbanning(true)
    try {
      await api.send(unbanUser, { userId: BigInt(userId) })
      toast(`已解封 ${u.nickname}，对方需要重新登录`)
      await refresh()
    } catch (e) {
      toast(errorText(e))
    } finally {
      setUnbanning(false)
    }
  }

  return (
    <section className="mt-6 flex flex-wrap items-center gap-4 rounded-xl border bg-card p-4">
      <UserAvatar seed={userId} name={u.nickname} size="lg" />
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <p className="truncate font-medium">{u.nickname || `用户 ${userId}`}</p>
          <span
            className={
              banned
                ? "shrink-0 rounded bg-destructive/10 px-1.5 py-px text-[11px] leading-4 font-medium text-destructive"
                : "shrink-0 rounded bg-muted px-1.5 py-px text-[11px] leading-4 font-medium text-muted-foreground"
            }
          >
            {banned ? "已封禁" : "正常"}
          </span>
        </div>
        <CopyId id={userId} className="mt-0.5" />
        {self && <p className="mt-1 text-xs text-muted-foreground">这是你自己的账号，不能封禁。</p>}
      </div>
      {banned ? (
        <Button variant="outline" disabled={unbanning} onClick={() => void unban()}>
          {unbanning ? <LoaderCircle className="animate-spin motion-reduce:animate-none" /> : <ShieldCheck />}
          解封
        </Button>
      ) : (
        <Button variant="destructive" disabled={self} onClick={() => setConfirming(true)}>
          <Ban />
          封禁
        </Button>
      )}

      <ConfirmDialog
        open={confirming}
        onOpenChange={setConfirming}
        title={`封禁 ${u.nickname || `用户 ${userId}`}`}
        description="对方的所有设备会立即下线，之后无法登录，直到解封。消息、好友和群不受影响。"
        confirmLabel="封禁"
        destructive
        onConfirm={async () => {
          await api.send(banUser, { userId: BigInt(userId) })
          toast(`已封禁 ${u.nickname}`)
          await refresh()
        }}
      />
    </section>
  )
}
