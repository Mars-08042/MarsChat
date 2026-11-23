import { Bell, BellOff } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { useNotifyPermission, type NotifyPermission } from "@/lib/notify-permission"

const STATUS_TEXT: Record<NotifyPermission, string> = {
  default: "还没有开启。点击「允许通知」后，浏览器会询问是否允许。",
  granted: "已开启。",
  denied: "已被浏览器阻止。如需开启，请在地址栏左侧的网站设置里把「通知」改为允许。",
  unsupported: "当前浏览器不支持系统通知。",
  insecure: "需要通过 HTTPS 访问 MarsChat 才能开启系统通知。",
}

// 通知（设计 3.6.8、3.6.10）：只有来电会发系统通知，并且只在标签页不可见时发送
export function NotificationsTab() {
  const { permission, request } = useNotifyPermission()
  const on = permission === "granted"

  const sendTest = () => {
    try {
      new Notification("MarsChat", { body: "通知已开启，标签页在后台时来电会这样提醒你", tag: "marschat-test" })
    } catch {
      // 部分平台（例如 Android 上的 Chrome）只允许经 Service Worker 发通知
      toast.error("当前浏览器无法直接显示通知")
    }
  }

  return (
    <div className="flex flex-col gap-3">
      <p className="text-sm text-muted-foreground">
        标签页在后台时，来电会弹出系统通知，点击通知回到 MarsChat。新消息不发系统通知，未读数显示在标签页标题上。
      </p>
      <section className="flex flex-wrap items-center gap-4 rounded-xl border bg-card p-4">
        <span
          aria-hidden="true"
          className={on ? "flex size-10 items-center justify-center rounded-full bg-brand/10 text-brand-text" : "flex size-10 items-center justify-center rounded-full bg-muted text-muted-foreground"}
        >
          {on ? <Bell className="size-5" /> : <BellOff className="size-5" />}
        </span>
        <div className="min-w-0 flex-1">
          <p className="font-medium">来电通知</p>
          <p className="text-sm text-muted-foreground">{STATUS_TEXT[permission]}</p>
        </div>
        {permission === "default" && <Button onClick={() => void request()}>允许通知</Button>}
        {on && (
          <Button variant="outline" onClick={sendTest}>
            发送测试通知
          </Button>
        )}
      </section>
    </div>
  )
}
