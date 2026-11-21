import { createBrowserRouter, Navigate } from "react-router"
import { AuthLayout } from "@/features/auth/AuthLayout"
import { LoginPage } from "@/features/auth/LoginPage"
import { RegisterPage } from "@/features/auth/RegisterPage"
import { ChatPage } from "@/features/chat/ChatPage"
import { AppShell } from "./AppShell"
import { ComingSoon } from "./ComingSoon"

// 路由表见设计 3.5。视觉原型阶段没有登录态，第 3 阶段加路由守卫和 redirect 参数
export const router = createBrowserRouter([
  { path: "/", element: <Navigate to="/login" replace /> },
  {
    element: <AuthLayout />,
    children: [
      { path: "/login", element: <LoginPage /> },
      { path: "/register", element: <RegisterPage /> },
    ],
  },
  {
    element: <AppShell />,
    children: [
      { path: "/chat/:conversationId?", element: <ChatPage /> },
      { path: "/contacts/*", element: <ComingSoon title="联系人" stage={5} /> },
      { path: "/groups", element: <ComingSoon title="群组" stage={5} /> },
      { path: "/settings/*", element: <ComingSoon title="设置" stage={6} /> },
    ],
  },
  { path: "*", element: <Navigate to="/login" replace /> },
])
