import { createBrowserRouter, Navigate, Outlet } from "react-router"
import { HOME_PATH } from "@/lib/redirect"
import { RedirectIfSignedIn, RequireAuth } from "./guards"
import { layouts, pages } from "./pages"
import { RouteError } from "./RouteError"

// 第一次打开页面时，布局的代码下载完成之前显示的占位，与布局的底色一致，避免闪白。
// 之后切换页面时停留在当前页面，新页面的代码下载完成再切换。
const authFallback = <div className="dark min-h-svh bg-background" />
const shellFallback = <div className="h-svh bg-background" />

// 路由表见设计 3.5。未登录时由 RequireAuth 跳到 /login?redirect=<原路径>。
// 布局和页面都按路由拆包（设计 2.3）：登录页不下载主框架、实时连接和各业务页面，
// 登录后不下载登录页的品牌区。代码下载失败（网络中断、发布新版本后旧文件不存在）时显示 RouteError。
export const router = createBrowserRouter([
  { path: "/", element: <Navigate to={HOME_PATH} replace /> },
  {
    element: (
      <RedirectIfSignedIn>
        <Outlet />
      </RedirectIfSignedIn>
    ),
    errorElement: <RouteError />,
    children: [
      {
        lazy: { Component: layouts.auth },
        hydrateFallbackElement: authFallback,
        children: [
          { path: "/login", lazy: { Component: pages.login } },
          { path: "/register", lazy: { Component: pages.register } },
        ],
      },
    ],
  },
  {
    element: (
      <RequireAuth>
        <Outlet />
      </RequireAuth>
    ),
    errorElement: <RouteError />,
    children: [
      {
        lazy: { Component: layouts.shell },
        hydrateFallbackElement: shellFallback,
        children: [
          { path: "/chat/:conversationId?", lazy: { Component: pages.chat } },
          // 和好友还没有会话时的草稿单聊，第一条消息确认后替换为 /chat/:conversationId
          { path: "/chat/with/:userId", lazy: { Component: pages.chat } },
          // 好友、申请、黑名单三个页签：/contacts、/contacts/requests、/contacts/blocked
          { path: "/contacts/*", lazy: { Component: pages.contacts } },
          { path: "/groups", lazy: { Component: pages.groups } },
          { path: "/settings/*", lazy: { Component: pages.settings } },
          // 封禁与解封，只有系统管理员可用（页面内再判断一次角色）
          { path: "/admin", lazy: { Component: pages.admin } },
        ],
      },
    ],
  },
  { path: "*", element: <Navigate to={HOME_PATH} replace /> },
])
