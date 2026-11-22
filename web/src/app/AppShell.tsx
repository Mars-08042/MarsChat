import { useEffect } from "react"
import { motion } from "motion/react"
import { NavLink, Outlet, useMatch } from "react-router"
import { CallLayer } from "@/features/call/CallLayer"
import { useChatStore } from "@/features/chat/fake-store"
import { springs } from "@/lib/motion"
import { cn } from "@/lib/utils"
import { useMe, useSession } from "@/stores/session"
import { NAV_ITEMS } from "./nav-items"
import { NavRail } from "./NavRail"
import { useUnreadTitle, useUnreadTotal } from "./use-unread-total"

// 登录后的主框架（设计 3.4.4）：768px 及以上左侧导航栏；以下改为底部标签栏，进入会话时整屏显示会话
export function AppShell() {
  const inConversation = useMatch("/chat/:conversationId") !== null
  useUnreadTitle()
  useCurrentUser()

  return (
    <div className="flex h-svh overflow-hidden bg-background text-foreground">
      <NavRail className="max-md:hidden" />
      <div className="flex min-w-0 flex-1 flex-col">
        <div className="flex min-h-0 flex-1">
          <Outlet />
        </div>
        {!inConversation && <MobileTabBar />}
      </div>
      <CallLayer />
    </div>
  )
}

// 刷新页面后登录状态只有令牌和用户 ID，在这里加载完整资料
function useCurrentUser() {
  const userId = useSession((s) => s.userId)
  const loaded = useSession((s) => s.user !== null)
  const loadMe = useSession((s) => s.loadMe)
  const me = useMe()

  useEffect(() => {
    // 失败时不重试：界面用「用户 <ID>」占位，不影响使用；令牌失效由令牌管理处理
    if (userId && !loaded) loadMe().catch(() => {})
  }, [userId, loaded, loadMe])

  // 聊天界面的过渡状态仍从 fake-store 读取当前用户，第 4 阶段由同步引擎取代
  const id = me?.id
  const nickname = me?.nickname
  useEffect(() => {
    if (id && nickname) useChatStore.setState({ me: { id, nickname } })
  }, [id, nickname])
}

function MobileTabBar() {
  const unread = useUnreadTotal()
  return (
    <nav aria-label="主导航" className="flex h-16 shrink-0 border-t bg-card pb-[env(safe-area-inset-bottom)] md:hidden">
      {NAV_ITEMS.map((item) => (
        <NavLink
          key={item.to}
          to={item.to}
          className={({ isActive }) =>
            cn(
              "relative flex flex-1 flex-col items-center justify-center gap-1 text-[11px] transition-colors",
              isActive ? "text-brand-text" : "text-muted-foreground",
            )
          }
        >
          {({ isActive }) => (
            <>
              {isActive && (
                <motion.span
                  layoutId="tab-active"
                  transition={springs.smooth}
                  className="absolute top-0 h-0.5 w-8 rounded-full bg-brand"
                />
              )}
              <span className="relative">
                <item.icon className="size-5" />
                {item.showUnread && unread > 0 && (
                  <span className="absolute -top-1.5 left-3 min-w-4 rounded-full bg-gold px-1 text-center font-mono text-[10px] leading-4 font-semibold text-gold-foreground">
                    {unread > 99 ? "99+" : unread}
                  </span>
                )}
              </span>
              {item.label}
            </>
          )}
        </NavLink>
      ))}
    </nav>
  )
}
