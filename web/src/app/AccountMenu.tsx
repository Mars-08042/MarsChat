import { useState } from "react"
import { Menu } from "@base-ui/react/menu"
import { LogOut, Settings } from "lucide-react"
import { useNavigate } from "react-router"
import { UserAvatar } from "@/components/common/UserAvatar"
import { useMe, useSession } from "@/stores/session"

const ITEM =
  "flex cursor-default items-center gap-2 rounded-md px-2 py-1.5 text-sm outline-none select-none data-disabled:opacity-50 data-highlighted:bg-accent data-highlighted:text-accent-foreground [&_svg]:size-4 [&_svg]:text-muted-foreground"

// 导航栏底部的头像菜单：显示当前账号，提供设置入口和退出登录
export function AccountMenu() {
  const me = useMe()
  const signOut = useSession((s) => s.signOut)
  const navigate = useNavigate()
  const [leaving, setLeaving] = useState(false)

  if (!me) return null

  const leave = async () => {
    setLeaving(true)
    // 退出后由路由守卫跳到登录页，组件随之卸载，不需要复位 leaving
    await signOut()
  }

  return (
    <Menu.Root>
      <Menu.Trigger
        aria-label="账号菜单"
        className="rounded-full outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background"
      >
        {/* 头像是 media 的文件 ID，换取下载地址在第 7 阶段接入，目前显示首字 */}
        <UserAvatar seed={me.id} name={me.nickname} size="md" className="ring-2 ring-border" />
      </Menu.Trigger>
      <Menu.Portal>
        <Menu.Positioner side="right" align="end" sideOffset={10} className="z-50 outline-none">
          <Menu.Popup className="min-w-56 origin-(--transform-origin) rounded-lg border bg-popover p-1 text-popover-foreground shadow-lg outline-none data-closed:animate-out data-closed:fade-out-0 data-closed:zoom-out-95 data-open:animate-in data-open:fade-in-0 data-open:zoom-in-95">
            <div className="px-2 py-1.5">
              <p className="truncate text-sm font-medium">{me.nickname}</p>
              {me.email && <p className="truncate text-xs text-muted-foreground">{me.email}</p>}
            </div>
            <Menu.Separator className="-mx-1 my-1 h-px bg-border" />
            <Menu.Item className={ITEM} onClick={() => navigate("/settings")}>
              <Settings />
              设置
            </Menu.Item>
            <Menu.Item className={ITEM} disabled={leaving} closeOnClick={false} onClick={leave}>
              <LogOut />
              {leaving ? "正在退出…" : "退出登录"}
            </Menu.Item>
          </Menu.Popup>
        </Menu.Positioner>
      </Menu.Portal>
    </Menu.Root>
  )
}
