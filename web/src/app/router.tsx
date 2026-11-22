import { createBrowserRouter, Navigate } from "react-router"
import { AuthLayout } from "@/features/auth/AuthLayout"
import { LoginPage } from "@/features/auth/LoginPage"
import { RegisterPage } from "@/features/auth/RegisterPage"
import { ChatPage } from "@/features/chat/ChatPage"
import { SettingsPage } from "@/features/settings/SettingsPage"
import { HOME_PATH } from "@/lib/redirect"
import { AppShell } from "./AppShell"
import { ComingSoon } from "./ComingSoon"
import { RedirectIfSignedIn, RequireAuth } from "./guards"

// 路由表见设计 3.5。未登录时由 RequireAuth 跳到 /login?redirect=<原路径>
export const router = createBrowserRouter([
  { path: "/", element: <Navigate to={HOME_PATH} replace /> },
  {
    element: (
      <RedirectIfSignedIn>
        <AuthLayout />
      </RedirectIfSignedIn>
    ),
    children: [
      { path: "/login", element: <LoginPage /> },
      { path: "/register", element: <RegisterPage /> },
    ],
  },
  {
    element: (
      <RequireAuth>
        <AppShell />
      </RequireAuth>
    ),
    children: [
      { path: "/chat/:conversationId?", element: <ChatPage /> },
      // 和好友还没有会话时的草稿单聊，第一条消息确认后替换为 /chat/:conversationId
      { path: "/chat/with/:userId", element: <ChatPage /> },
      { path: "/contacts/*", element: <ComingSoon title="联系人" stage={5} /> },
      { path: "/groups", element: <ComingSoon title="群组" stage={5} /> },
      { path: "/settings/*", element: <SettingsPage /> },
    ],
  },
  { path: "*", element: <Navigate to={HOME_PATH} replace /> },
])
