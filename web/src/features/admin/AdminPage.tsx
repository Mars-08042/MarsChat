import { useEffect, useState, type ReactNode } from "react"
import { useSearchParams } from "react-router"
import { keepPreviousData, useQuery } from "@tanstack/react-query"
import { timestampMs } from "@bufbuild/protobuf/wkt"
import { Ban, ChevronLeft, ChevronRight, LoaderCircle, Search, ShieldCheck } from "lucide-react"
import { toast } from "sonner"
import { api } from "@/api/client"
import { banUser, listUsers, unbanUser } from "@/api/endpoints"
import { errorText } from "@/api/errors"
import { ConfirmDialog } from "@/components/common/ConfirmDialog"
import { CopyId } from "@/components/common/CopyId"
import { ListState } from "@/components/common/ListState"
import { UserAvatar } from "@/components/common/UserAvatar"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Choice } from "@/features/settings/Choice"
import { Role, UserStatus, type User } from "@/gen/account/v1/account_pb"
import { queryClient, queryKeys } from "@/lib/query"
import { formatDate } from "@/lib/time"
import { cn } from "@/lib/utils"
import { useSession } from "@/stores/session"
import { PAGE_SIZE, STATUS_FILTERS, pageCount, parseStatusFilter, statusOf, type StatusFilter } from "./model"

const SEARCH_DEBOUNCE_MS = 300

// 管理页（设计 3.5，account 设计第 9 节）：分页列出用户，按 ID、邮箱、昵称和状态筛选，封禁或解封。
// 导航栏只对系统管理员显示入口；直接打开地址时，非管理员看到没有权限的说明
export function AdminPage() {
  const role = useSession((s) => s.user?.role)
  const [params, setParams] = useSearchParams()
  const keyword = params.get("q") ?? ""
  const filter = parseStatusFilter(params.get("status"))

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

  const update = (next: { q?: string; status?: StatusFilter }) => {
    const q = next.q ?? keyword
    const status = next.status ?? filter
    const p = new URLSearchParams()
    if (q) p.set("q", q)
    if (status !== "all") p.set("status", status)
    setParams(p, { replace: true })
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-y-auto [scrollbar-width:thin]">
      <div className="mx-auto w-full max-w-5xl px-4 pt-6 pb-10 md:px-8 md:pt-10">
        <h1 className="font-display text-2xl font-semibold">管理</h1>
        <p className="mt-1 text-sm text-muted-foreground">封禁后对方的所有设备立即下线，之后无法登录，直到解封。</p>

        <div className="mt-6 flex flex-col gap-3 sm:flex-row sm:items-center">
          <SearchBox keyword={keyword} onSearch={(q) => update({ q })} />
          <Choice label="账号状态" value={filter} options={STATUS_FILTERS} onPick={(status) => update({ status })} className="sm:w-72" />
        </div>

        {/* 筛选条件变化时重新从第一页开始 */}
        <UserList key={`${keyword}\n${filter}`} keyword={keyword} filter={filter} />
      </div>
    </div>
  )
}

// 停止输入 300 毫秒后查询，回车立即查询。纯数字按 ID 查，含 @ 按邮箱开头查，其他按昵称或邮箱开头查
function SearchBox({ keyword, onSearch }: { keyword: string; onSearch: (q: string) => void }) {
  const [text, setText] = useState(keyword)
  // 后退等方式改了地址里的关键词时，输入框跟着变；自己输入引起的变化两者已经一致
  const [synced, setSynced] = useState(keyword)
  if (keyword !== synced) {
    setSynced(keyword)
    if (text.trim() !== keyword) setText(keyword)
  }

  const q = text.trim()
  useEffect(() => {
    if (q === keyword) return
    const timer = setTimeout(() => onSearch(q), SEARCH_DEBOUNCE_MS)
    return () => clearTimeout(timer)
  }, [q, keyword, onSearch])

  return (
    <form
      role="search"
      className="relative flex-1"
      onSubmit={(e) => {
        e.preventDefault()
        if (q !== keyword) onSearch(q)
      }}
    >
      <Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground" />
      <label htmlFor="admin-search" className="sr-only">
        搜索用户
      </label>
      <Input
        id="admin-search"
        type="search"
        placeholder="用户 ID、邮箱或昵称"
        value={text}
        maxLength={254}
        onChange={(e) => setText(e.target.value)}
        className="pl-9"
      />
    </form>
  )
}

function UserList({ keyword, filter }: { keyword: string; filter: StatusFilter }) {
  const meId = useSession((s) => s.userId)
  const status = statusOf(filter)
  // 游标分页（account 设计 9.3）：记住走过的每一页的游标，用来返回上一页；最后一个是当前页
  const [cursors, setCursors] = useState<bigint[]>([0n])
  const cursor = cursors[cursors.length - 1]
  const [banTarget, setBanTarget] = useState<User>()
  const [unbanning, setUnbanning] = useState<bigint>()

  const page = useQuery({
    queryKey: queryKeys.adminUserPage(keyword, status, cursor),
    queryFn: () => api.send(listUsers, { keyword, status, cursor, limit: PAGE_SIZE }),
    placeholderData: keepPreviousData,
    // 账号状态随时可能被别的管理员改，每次打开都重新获取
    staleTime: 0,
  })

  // 在「正常」「已封禁」下操作后，当前页可能变空，退回上一页
  if (page.data && !page.isPlaceholderData && page.data.users.length === 0 && cursors.length > 1) {
    setCursors(cursors.slice(0, -1))
  }

  const refresh = (userId: bigint) =>
    Promise.all([
      queryClient.invalidateQueries({ queryKey: queryKeys.adminUsers }),
      queryClient.invalidateQueries({ queryKey: queryKeys.user(userId.toString()) }),
    ])

  const unban = async (u: User) => {
    setUnbanning(u.id)
    try {
      await api.send(unbanUser, { userId: u.id })
      toast(`已解封 ${displayName(u)}，对方需要重新登录`)
      await refresh(u.id)
    } catch (e) {
      toast(errorText(e))
    } finally {
      setUnbanning(undefined)
    }
  }

  const users = page.data?.users ?? []
  const total = page.data?.total ?? 0
  const filtered = keyword !== "" || filter !== "all"
  const actions = (u: User) => (
    <RowAction user={u} self={u.id.toString() === meId} unbanning={unbanning === u.id} onBan={() => setBanTarget(u)} onUnban={() => void unban(u)} />
  )

  return (
    <section aria-label="用户列表" className="mt-4 overflow-hidden rounded-xl border bg-card">
      <ListState query={page} empty={users.length === 0 ? (filtered ? "没有找到匹配的用户" : "还没有用户") : undefined}>
        <div className={cn("transition-opacity", page.isPlaceholderData && "opacity-60")} aria-busy={page.isPlaceholderData}>
          <table className="hidden w-full text-sm md:table">
            <thead className="border-b text-left text-xs text-muted-foreground">
              <tr>
                <th className="px-4 py-2.5 font-medium">用户</th>
                <th className="px-4 py-2.5 font-medium">邮箱</th>
                <th className="px-4 py-2.5 font-medium">ID</th>
                <th className="px-4 py-2.5 font-medium">状态</th>
                <th className="px-4 py-2.5 font-medium">注册时间</th>
                <th className="px-4 py-2.5">
                  <span className="sr-only">操作</span>
                </th>
              </tr>
            </thead>
            <tbody className="divide-y">
              {users.map((u) => (
                <tr key={u.id.toString()} className="hover:bg-muted/40">
                  <td className="px-4 py-3">
                    <UserName user={u} self={u.id.toString() === meId} />
                  </td>
                  <td className="max-w-56 truncate px-4 py-3 text-muted-foreground" title={u.email}>
                    {u.email}
                  </td>
                  <td className="px-4 py-3">
                    <CopyId id={u.id.toString()} />
                  </td>
                  <td className="px-4 py-3">
                    <StatusBadge status={u.status} />
                  </td>
                  <td className="px-4 py-3 text-muted-foreground tabular-nums">{u.createdAt ? formatDate(timestampMs(u.createdAt)) : ""}</td>
                  <td className="px-4 py-3 text-right">{actions(u)}</td>
                </tr>
              ))}
            </tbody>
          </table>

          <ul className="divide-y md:hidden">
            {users.map((u) => (
              <li key={u.id.toString()} className="flex items-start gap-3 p-4">
                <div className="min-w-0 flex-1">
                  <UserName user={u} self={u.id.toString() === meId} badge={<StatusBadge status={u.status} />} />
                  <p className="mt-1.5 truncate text-sm text-muted-foreground">{u.email}</p>
                  <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
                    <CopyId id={u.id.toString()} />
                    {u.createdAt && <span className="tabular-nums">注册于 {formatDate(timestampMs(u.createdAt))}</span>}
                  </div>
                </div>
                {actions(u)}
              </li>
            ))}
          </ul>
        </div>

        <footer className="flex flex-wrap items-center justify-between gap-3 border-t px-4 py-3 text-sm text-muted-foreground">
          <span>共 {total} 人</span>
          <nav aria-label="分页" className="flex items-center gap-2">
            <Button variant="outline" size="sm" disabled={cursors.length === 1 || page.isPlaceholderData} onClick={() => setCursors(cursors.slice(0, -1))}>
              <ChevronLeft />
              上一页
            </Button>
            <span className="tabular-nums" aria-live="polite">
              第 {cursors.length} / {Math.max(cursors.length, pageCount(total))} 页
            </span>
            <Button
              variant="outline"
              size="sm"
              disabled={!page.data?.nextCursor || page.isPlaceholderData}
              onClick={() => page.data && setCursors([...cursors, page.data.nextCursor])}
            >
              下一页
              <ChevronRight />
            </Button>
          </nav>
        </footer>
      </ListState>

      <ConfirmDialog
        open={banTarget !== undefined}
        onOpenChange={(open) => !open && setBanTarget(undefined)}
        title={`封禁 ${banTarget ? displayName(banTarget) : ""}`}
        description="对方的所有设备会立即下线，之后无法登录，直到解封。消息、好友和群不受影响。"
        confirmLabel="封禁"
        destructive
        onConfirm={async () => {
          if (!banTarget) return
          await api.send(banUser, { userId: banTarget.id })
          toast(`已封禁 ${displayName(banTarget)}`)
          await refresh(banTarget.id)
        }}
      />
    </section>
  )
}

const displayName = (u: User) => u.nickname || `用户 ${u.id}`

function UserName({ user, self, badge }: { user: User; self: boolean; badge?: ReactNode }) {
  return (
    <div className="flex min-w-0 items-center gap-2.5">
      <UserAvatar seed={user.id.toString()} name={user.nickname} size="sm" />
      <span className="truncate font-medium">{displayName(user)}</span>
      {self && <Tag className="bg-muted text-muted-foreground">你</Tag>}
      {user.role === Role.ADMIN && <Tag className="bg-brand/10 text-brand-text">管理员</Tag>}
      {badge}
    </div>
  )
}

function StatusBadge({ status }: { status: UserStatus }) {
  return status === UserStatus.BANNED ? <Tag className="bg-destructive/10 text-destructive">已封禁</Tag> : <Tag className="bg-muted text-muted-foreground">正常</Tag>
}

function Tag({ className, children }: { className: string; children: ReactNode }) {
  return <span className={cn("shrink-0 rounded px-1.5 py-px text-[11px] leading-4 font-medium", className)}>{children}</span>
}

function RowAction({ user, self, unbanning, onBan, onUnban }: { user: User; self: boolean; unbanning: boolean; onBan: () => void; onUnban: () => void }) {
  if (user.status === UserStatus.BANNED) {
    return (
      <Button variant="outline" size="sm" disabled={unbanning} onClick={onUnban}>
        {unbanning ? <LoaderCircle className="animate-spin motion-reduce:animate-none" /> : <ShieldCheck />}
        解封
      </Button>
    )
  }
  return (
    <Button
      variant="outline"
      size="sm"
      disabled={self}
      title={self ? "不能封禁自己的账号" : undefined}
      onClick={onBan}
      className="text-destructive hover:bg-destructive/10 hover:text-destructive"
    >
      <Ban />
      封禁
    </Button>
  )
}
