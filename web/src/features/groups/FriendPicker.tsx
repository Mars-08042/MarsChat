import { useMemo, useState } from "react"
import { Check, Search } from "lucide-react"
import { ListState } from "@/components/common/ListState"
import { UserAvatar } from "@/components/common/UserAvatar"
import { Input } from "@/components/ui/input"
import { useFriends } from "@/features/contacts/queries"
import { cn } from "@/lib/utils"

interface FriendPickerProps {
  selected: ReadonlySet<string>
  onToggle: (userId: string) => void
  // 已经在群里的人显示为已选中且不能取消
  disabledIds?: ReadonlySet<string>
  max: number
}

// 建群和邀请入群时多选好友；只能选好友（AddGroupMembers、CreateGroup 都要求是操作者的好友）
export function FriendPicker({ selected, onToggle, disabledIds, max }: FriendPickerProps) {
  const friends = useFriends()
  const [query, setQuery] = useState("")
  const shown = useMemo(() => {
    const keyword = query.trim().toLowerCase()
    const list = friends.data ?? []
    return keyword ? list.filter((f) => f.user.nickname.toLowerCase().includes(keyword) || f.user.id === keyword) : list
  }, [friends.data, query])
  const full = selected.size >= max

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-2">
      <div className="relative">
        <Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground" />
        <Input
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="搜索好友昵称或 ID"
          aria-label="搜索好友"
          className="h-9 rounded-lg border-transparent bg-muted pl-9 dark:bg-muted"
        />
      </div>
      <div className="min-h-48 flex-1 overflow-y-auto [scrollbar-width:thin]">
        <ListState
          query={friends}
          empty={friends.data?.length === 0 ? "还没有好友，先去联系人页添加吧" : shown.length === 0 ? "没有找到相关好友" : undefined}
        >
          <ul className="space-y-0.5">
            {shown.map((f) => {
              const locked = disabledIds?.has(f.user.id) ?? false
              const checked = locked || selected.has(f.user.id)
              const disabled = locked || (!checked && full)
              return (
                <li key={f.user.id}>
                  <button
                    type="button"
                    role="checkbox"
                    aria-checked={checked}
                    disabled={disabled}
                    onClick={() => onToggle(f.user.id)}
                    className="flex w-full items-center gap-3 rounded-xl px-2 py-2 text-left transition-colors outline-none hover:bg-muted/70 focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-60"
                  >
                    <span
                      className={cn(
                        "flex size-5 shrink-0 items-center justify-center rounded-md border transition-colors",
                        checked ? "border-brand-strong bg-brand-strong text-white" : "border-input",
                      )}
                    >
                      {checked && <Check className="size-3.5" strokeWidth={3} />}
                    </span>
                    <UserAvatar seed={f.user.id} name={f.user.nickname} avatarId={f.user.avatarId} size="sm" />
                    <span className="min-w-0 flex-1 truncate text-sm">{f.user.nickname}</span>
                    {locked && <span className="shrink-0 text-xs text-muted-foreground">已在群里</span>}
                  </button>
                </li>
              )
            })}
          </ul>
        </ListState>
      </div>
      <p className="text-xs text-muted-foreground">
        已选 {selected.size} 人{full && `，最多 ${max} 人`}
      </p>
    </div>
  )
}
