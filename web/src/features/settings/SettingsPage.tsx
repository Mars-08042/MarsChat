import { useState } from "react"
import { LoaderCircle, LogOut } from "lucide-react"
import { UserAvatar } from "@/components/common/UserAvatar"
import { Button } from "@/components/ui/button"
import { useMe, useSession } from "@/stores/session"

// 设置页的过渡版本：资料、设备、外观在第 6 阶段实现。
// 先放账号信息和退出登录，窄屏没有导航栏头像菜单，只能从这里退出
export function SettingsPage() {
  const me = useMe()
  const signOut = useSession((s) => s.signOut)
  const [leaving, setLeaving] = useState(false)

  const leave = async () => {
    setLeaving(true)
    await signOut()
  }

  return (
    <div className="flex flex-1 flex-col items-center overflow-y-auto p-6 md:p-10">
      <div className="w-full max-w-md space-y-6">
        <h1 className="font-display text-2xl font-semibold">设置</h1>

        {me && (
          <section className="flex items-center gap-4 rounded-xl border bg-card p-4">
            <UserAvatar seed={me.id} name={me.nickname} size="lg" />
            <div className="min-w-0 flex-1">
              <p className="truncate font-medium">{me.nickname}</p>
              {me.email && <p className="truncate text-sm text-muted-foreground">{me.email}</p>}
            </div>
          </section>
        )}

        <p className="text-sm text-muted-foreground">资料、登录设备与外观设置计划在第 6 阶段实现。</p>

        <Button variant="outline" className="h-10 w-full" disabled={leaving} onClick={leave}>
          {leaving ? <LoaderCircle className="animate-spin motion-reduce:animate-none" /> : <LogOut />}
          退出登录
        </Button>
      </div>
    </div>
  )
}
