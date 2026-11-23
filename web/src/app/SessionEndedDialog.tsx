import { useState } from "react"
import { AlertDialog } from "@base-ui/react/alert-dialog"
import type { EndReason } from "@/api/tokens"
import { Button } from "@/components/ui/button"
import { useSession } from "@/stores/session"

type ShownReason = Exclude<EndReason, "logout">

const TEXT: Record<ShownReason, { title: string; description: string }> = {
  expired: { title: "登录已过期", description: "为了账号安全，请重新登录。" },
  banned: { title: "账号已被封禁", description: "这个账号已被管理员封禁，暂时无法使用。如有疑问请联系管理员。" },
  // 还没有修改密码的功能（features.md），只能引导到登录设备页处理
  kicked: {
    title: "已在其他设备上被移出",
    description: "你的账号在其他设备上让这台设备下线了。如果不是你本人操作，重新登录后可以在「设置 → 登录设备」里让陌生设备下线。",
  },
  replaced: { title: "账号已重新登录", description: "这台设备上的账号重新登录了，当前页面已退出。" },
  reuse: { title: "登录凭证异常", description: "检测到登录凭证被重复使用，为了账号安全已退出，请重新登录。" },
  revoked: { title: "登录已失效", description: "这个登录已在服务端失效，请重新登录。" },
}

// 登录状态被动结束时的全屏遮罩（设计 3.6.6）：刷新令牌失效，或长连接收到 Kicked。
// 本地登录状态已经清除，确认后由路由守卫跳到登录页
export function SessionEndedDialog() {
  const ended = useSession((s) => s.ended)
  const dismiss = useSession((s) => s.dismissEnded)
  const reason = ended && ended !== "logout" ? ended : null
  // 关闭动画期间沿用上一次的文案
  const [shown, setShown] = useState<ShownReason>("expired")
  if (reason && reason !== shown) setShown(reason)
  const text = TEXT[reason ?? shown]

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
