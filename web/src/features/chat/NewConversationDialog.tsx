import { useEffect, useMemo, useState } from "react"
import { Dialog } from "@base-ui/react/dialog"
import { useNavigate } from "react-router"
import { ChevronRight, Search, UsersRound, X } from "lucide-react"
import { UserAvatar } from "@/components/common/UserAvatar"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { errorText } from "@/api/errors"
import { chat } from "@/realtime/client"
import type { ChatUser } from "./types"

type Load = { kind: "loading" } | { kind: "error"; message: string } | { kind: "done"; friends: ChatUser[] }

// 新建会话：从好友里选一位，进入草稿单聊 /chat/with/:userId；已有会话时由 ChatPage 跳到原会话。
// 顶部的「创建群聊」关闭这个对话框，打开建群向导
export function NewConversationDialog({
  open,
  onOpenChange,
  onCreateGroup,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  onCreateGroup: () => void
}) {
  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Backdrop className="fixed inset-0 z-50 bg-black/40 backdrop-blur-sm transition-opacity duration-200 data-ending-style:opacity-0 data-starting-style:opacity-0" />
        <Dialog.Popup className="fixed top-1/2 left-1/2 z-50 flex max-h-[min(36rem,calc(100dvh-2rem))] w-[min(26rem,calc(100vw-2rem))] -translate-x-1/2 -translate-y-1/2 flex-col rounded-xl border bg-popover text-popover-foreground shadow-xl transition-all duration-200 outline-none data-ending-style:scale-95 data-ending-style:opacity-0 data-starting-style:scale-95 data-starting-style:opacity-0">
          <div className="flex items-center justify-between px-5 pt-5 pb-3">
            <Dialog.Title className="font-display text-lg font-semibold">发起聊天</Dialog.Title>
            <Dialog.Close render={<Button variant="ghost" size="icon" aria-label="关闭" />}>
              <X />
            </Dialog.Close>
          </div>
          <div className="px-5 pb-3">
            <button
              type="button"
              onClick={() => {
                onOpenChange(false)
                onCreateGroup()
              }}
              className="flex w-full items-center gap-3 rounded-xl border px-3 py-2.5 text-left transition-colors outline-none hover:bg-muted/70 focus-visible:ring-2 focus-visible:ring-ring"
            >
              <span className="flex size-9 items-center justify-center rounded-[30%] bg-brand/10 text-brand">
                <UsersRound className="size-5" />
              </span>
              <span className="flex-1 text-sm font-medium">创建群聊</span>
              <ChevronRight className="size-4 text-muted-foreground" />
            </button>
          </div>
          {/* 每次打开重新加载，好友列表可能在其他设备上变化 */}
          {open && <FriendPicker onPicked={() => onOpenChange(false)} />}
        </Dialog.Popup>
      </Dialog.Portal>
    </Dialog.Root>
  )
}

function FriendPicker({ onPicked }: { onPicked: () => void }) {
  const navigate = useNavigate()
  const [load, setLoad] = useState<Load>({ kind: "loading" })
  const [attempt, setAttempt] = useState(0)
  const [query, setQuery] = useState("")

  useEffect(() => {
    let cancelled = false
    chat.loadFriends().then(
      (friends) => !cancelled && setLoad({ kind: "done", friends }),
      (e: unknown) => !cancelled && setLoad({ kind: "error", message: errorText(e) }),
    )
    return () => {
      cancelled = true
    }
  }, [attempt])

  const shown = useMemo(() => {
    if (load.kind !== "done") return []
    const keyword = query.trim().toLowerCase()
    return load.friends.filter((f) => keyword === "" || f.nickname.toLowerCase().includes(keyword) || f.id === keyword)
  }, [load, query])

  const pick = (friend: ChatUser) => {
    onPicked()
    navigate(`/chat/with/${friend.id}`)
  }

  return (
    <>
      <div className="px-5 pb-3">
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
      </div>
      <div className="min-h-40 flex-1 overflow-y-auto px-2 pb-3 [scrollbar-width:thin]">
        {load.kind === "loading" && <p className="py-10 text-center text-sm text-muted-foreground">正在加载好友…</p>}
        {load.kind === "error" && (
          <div className="flex flex-col items-center gap-3 py-10 text-sm text-muted-foreground">
            <p>{load.message}</p>
            <Button
              variant="outline"
              size="sm"
              onClick={() => {
                setLoad({ kind: "loading" })
                setAttempt((n) => n + 1)
              }}
            >
              重试
            </Button>
          </div>
        )}
        {load.kind === "done" && shown.length === 0 && (
          <p className="py-10 text-center text-sm text-muted-foreground">{load.friends.length === 0 ? "还没有好友" : "没有找到相关好友"}</p>
        )}
        <ul className="space-y-0.5">
          {shown.map((f) => (
            <li key={f.id}>
              <button
                type="button"
                onClick={() => pick(f)}
                className="flex w-full items-center gap-3 rounded-xl px-3 py-2 text-left transition-colors outline-none hover:bg-muted/70 focus-visible:ring-2 focus-visible:ring-ring"
              >
                <UserAvatar seed={f.id} name={f.nickname} avatarId={f.avatarId} />
                <div className="min-w-0">
                  <p className="truncate font-medium">{f.nickname}</p>
                  <p className="font-mono text-xs text-muted-foreground">ID {f.id}</p>
                </div>
              </button>
            </li>
          ))}
        </ul>
      </div>
    </>
  )
}
