import { AlertDialog } from "@base-ui/react/alert-dialog"
import { Button } from "@/components/ui/button"
import { useSession } from "@/stores/session"

const TEXT = {
  expired: { title: "登录已过期", description: "为了账号安全，请重新登录。" },
  banned: { title: "账号已被封禁", description: "这个账号已被管理员封禁，暂时无法使用。如有疑问请联系管理员。" },
} as const

// 刷新令牌失效或用户被封禁时的全屏遮罩（设计 3.6.6）。本地登录状态已经清除，确认后由路由守卫跳到登录页。
// 被踢下线（WebSocket 4002）在第 4 阶段接入连接状态机后复用这里
export function SessionEndedDialog() {
  const ended = useSession((s) => s.ended)
  const dismiss = useSession((s) => s.dismissEnded)
  const reason = ended === "expired" || ended === "banned" ? ended : null
  // 关闭动画期间保留文案
  const text = TEXT[reason ?? "expired"]

  return (
    <AlertDialog.Root open={reason !== null} onOpenChange={(open) => !open && dismiss()}>
      <AlertDialog.Portal>
        <AlertDialog.Backdrop className="fixed inset-0 z-50 bg-black/50 backdrop-blur-sm transition-opacity duration-200 data-ending-style:opacity-0 data-starting-style:opacity-0" />
        <AlertDialog.Popup className="fixed top-1/2 left-1/2 z-50 w-[min(24rem,calc(100vw-2rem))] -translate-x-1/2 -translate-y-1/2 rounded-xl border bg-popover p-6 text-popover-foreground shadow-xl transition-all duration-200 outline-none data-ending-style:scale-95 data-ending-style:opacity-0 data-starting-style:scale-95 data-starting-style:opacity-0">
          <AlertDialog.Title className="font-display text-lg font-semibold">{text.title}</AlertDialog.Title>
          <AlertDialog.Description className="mt-2 text-sm text-muted-foreground">{text.description}</AlertDialog.Description>
          <div className="mt-6 flex justify-end">
            <AlertDialog.Close render={<Button />}>重新登录</AlertDialog.Close>
          </div>
        </AlertDialog.Popup>
      </AlertDialog.Portal>
    </AlertDialog.Root>
  )
}
