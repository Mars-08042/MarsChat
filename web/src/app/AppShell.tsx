import { useEffect, useLayoutEffect } from "react"
import { motion } from "motion/react"
import { NavLink, Outlet, useMatch, useNavigate } from "react-router"
import { downloadUrls } from "@/api/client"
import { Celebration } from "@/components/common/Celebration"
import { CallLayer } from "@/features/call/CallLayer"
import { springs } from "@/lib/motion"
import { queryClient } from "@/lib/query"
import { cn } from "@/lib/utils"
import { startRealtime } from "@/realtime/client"
import { useSession } from "@/stores/session"
import { useUi } from "@/stores/ui"
import { useNavItems, type NavItem } from "./nav-items"
import { NavRail } from "./NavRail"
import { preloadAppPages } from "./pages"
import { useNavBadge, useUnreadTitle } from "./use-unread-total"

// 登录后的主框架（设计 3.4.4）：768px 及以上左侧导航栏；以下改为底部标签栏，进入会话时整屏显示会话
export function AppShell() {
  const inConversation = useMatch("/chat/:conversationId") !== null
  useUnreadTitle()
  useCurrentUser()
  useRealtime()
  useRegisterNavigate()
  usePreloadPages()

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
      <Celebration />
    </div>
  )
}

// 刷新页面后登录状态只有令牌和用户 ID，在这里加载完整资料
function useCurrentUser() {
  const userId = useSession((s) => s.userId)
  const loaded = useSession((s) => s.user !== null)
  const loadMe = useSession((s) => s.loadMe)

  useEffect(() => {
    // 失败时不重试：界面用「用户 <ID>」占位，不影响使用；令牌失效由令牌管理处理
    if (userId && !loaded) loadMe().catch(() => {})
  }, [userId, loaded, loadMe])
}

// 登录期间保持实时连接；登录状态结束（userId 变为空）或换了账号时停止。
// 用 layout effect：子页面（ChatPage）在 effect 里打开会话，要在实时客户端启动之后
// 让组件树之外的代码（通知提示的按钮）也能跳转路由
function useRegisterNavigate() {
  const navigate = useNavigate()
  useEffect(() => {
    useUi.setState({ navigate: (to) => void navigate(to) })
    return () => useUi.setState({ navigate: null })
  }, [navigate])
}

function useRealtime() {
  const userId = useSession((s) => s.userId)
  useLayoutEffect(() => {
    if (!userId) return
    // 好友、群等查询缓存和下载地址属于上一个账号；被踢下线时保留到重新登录，这里统一清掉
    queryClient.clear()
    downloadUrls.clear()
    const stop = startRealtime(userId)
    return () => {
      const ended = useSession.getState().ended
      const keepData = ended !== null && ended !== "logout"
      stop({ keepData })
      if (!keepData) {
        queryClient.clear()
        downloadUrls.clear()
      }
    }
  }, [userId])
}

// 当前页面加载完、实时连接建立之后再预取其他页面，不和首屏的请求抢带宽
const PRELOAD_DELAY_MS = 2000

function usePreloadPages() {
  useEffect(() => {
    const timer = setTimeout(preloadAppPages, PRELOAD_DELAY_MS)
    return () => clearTimeout(timer)
  }, [])
}

function MobileTabBar() {
  const items = useNavItems()
  return (
    <nav aria-label="主导航" className="flex h-16 shrink-0 border-t bg-card pb-[env(safe-area-inset-bottom)] md:hidden">
      {items.map((item) => (
        <MobileTab key={item.to} item={item} />
      ))}
    </nav>
  )
}

function MobileTab({ item }: { item: NavItem }) {
  const count = useNavBadge(item.badge)
  return (
    <NavLink
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
            {count > 0 && (
              <span className="absolute -top-1.5 left-3 min-w-4 rounded-full bg-gold px-1 text-center font-mono text-[10px] leading-4 font-semibold text-gold-foreground">
                {count > 99 ? "99+" : count}
              </span>
            )}
          </span>
          {item.label}
        </>
      )}
    </NavLink>
  )
}
