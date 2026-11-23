import type { ReactNode } from "react"
import { Navigate, useLocation, useSearchParams } from "react-router"
import { loginPath, safeRedirect } from "@/lib/redirect"
import { useSession } from "@/stores/session"

// 受保护的页面：未登录时跳到登录页并带上原路径
export function RequireAuth({ children }: { children: ReactNode }) {
  const signedIn = useSession((s) => s.signedIn)
  const ended = useSession((s) => s.ended)
  const location = useLocation()

  if (signedIn) return children
  // 主动退出后回到登录页，不带原路径，换个账号登录时不会跳到上一个账号的会话
  if (ended === "logout") return <Navigate to="/login" replace />
  // 登录状态被动结束（过期、被踢、封禁）时先留在原页面显示失效提示，确认后再跳转（设计 3.6.6）
  if (ended) return children
  return <Navigate to={loginPath(location)} replace />
}

// 登录、注册页：已登录时直接进入应用。登录成功后也由这里跳转到 redirect 参数指定的页面
export function RedirectIfSignedIn({ children }: { children: ReactNode }) {
  const signedIn = useSession((s) => s.signedIn)
  const [params] = useSearchParams()

  if (signedIn) return <Navigate to={safeRedirect(params.get("redirect"))} replace />
  return children
}
