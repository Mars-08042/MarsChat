import { useState } from "react"
import { Navigate, useLocation } from "react-router"
import { LoaderCircle, LogOut } from "lucide-react"
import { CopyId } from "@/components/common/CopyId"
import { PageTabs } from "@/components/common/PageTabs"
import { UserAvatar } from "@/components/common/UserAvatar"
import { Button } from "@/components/ui/button"
import { Role } from "@/gen/account/v1/account_pb"
import { useMe, useSession } from "@/stores/session"
import { AppearanceTab } from "./AppearanceTab"
import { DevicesTab } from "./DevicesTab"
import { ProfileTab } from "./ProfileTab"

const TABS = [
  { to: "/settings/profile", label: "资料" },
  { to: "/settings/devices", label: "登录设备" },
  { to: "/settings/appearance", label: "外观" },
] as const

// 设置：资料、登录设备、外观三个页签对应三个路由（设计 3.5、3.6.8）。
// 窄屏没有导航栏的头像菜单，退出登录放在账号卡片里
export function SettingsPage() {
  const { pathname } = useLocation()
  const active = TABS.find((t) => pathname.startsWith(t.to))?.to

  if (!active) return <Navigate to="/settings/profile" replace />

  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-y-auto [scrollbar-width:thin]">
      <div className="mx-auto w-full max-w-2xl px-4 pt-6 pb-10 md:px-8 md:pt-10">
        <h1 className="font-display text-2xl font-semibold">设置</h1>
        <AccountCard />
        <div className="mt-6">
          <PageTabs label="设置分类" tabs={TABS} active={active} layoutId="settings-tab" />
        </div>
        <div className="mt-5">
          {active === "/settings/profile" && <ProfileTab />}
          {active === "/settings/devices" && <DevicesTab />}
          {active === "/settings/appearance" && <AppearanceTab />}
        </div>
      </div>
    </div>
  )
}

function AccountCard() {
  const me = useMe()
  const admin = useSession((s) => s.user?.role === Role.ADMIN)
  const signOut = useSession((s) => s.signOut)
  const [leaving, setLeaving] = useState(false)

  if (!me) return null

  const leave = async () => {
    setLeaving(true)
    await signOut()
  }

  return (
    <section className="mt-5 flex flex-wrap items-center gap-4 rounded-xl border bg-card p-4">
      <UserAvatar seed={me.id} name={me.nickname} size="lg" />
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <p className="truncate font-medium">{me.nickname}</p>
          {admin && <span className="shrink-0 rounded px-1.5 py-px text-[11px] leading-4 font-medium bg-brand/10 text-brand-text">系统管理员</span>}
        </div>
        {me.email && <p className="truncate text-sm text-muted-foreground">{me.email}</p>}
        <CopyId id={me.id} className="mt-0.5" />
      </div>
      <Button variant="outline" disabled={leaving} onClick={() => void leave()}>
        {leaving ? <LoaderCircle className="animate-spin motion-reduce:animate-none" /> : <LogOut />}
        退出登录
      </Button>
    </section>
  )
}
