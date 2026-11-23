// 登录状态（设计 3.8）：以令牌管理为准，这里只放界面需要的派生状态和当前用户的资料。

import { create } from "zustand"
import { api, tokens } from "@/api/client"
import { getMe, logout } from "@/api/endpoints"
import { ApiError, UNKNOWN } from "@/api/errors"
import type { EndReason, TokenGrant } from "@/api/tokens"
import type { User } from "@/gen/account/v1/account_pb"

interface SessionState {
  signedIn: boolean
  userId: string | null
  // GetMe 的完整资料；登录、注册的响应里直接带回，刷新页面后由 loadMe 加载
  user: User | null
  // 最近一次登录状态结束的原因，重新登录后清空。
  // expired、banned 时显示失效提示，用户确认后清空并跳到登录页；logout 不提示
  ended: EndReason | null

  // 登录、注册成功后调用，参数是接口的响应
  signIn: (res: { user?: User; token?: TokenGrant }) => void
  signOut: () => Promise<void>
  loadMe: () => Promise<void>
  dismissEnded: () => void
}

const initial = tokens.current()

export const useSession = create<SessionState>()((set, get) => ({
  signedIn: initial !== null,
  userId: initial?.userId ?? null,
  user: null,
  ended: null,

  signIn: ({ user, token }) => {
    if (!user || !token) throw new ApiError(500, UNKNOWN, "sign-in response without user or token")
    // 先写入 userId，令牌管理随后通知时识别为同一个用户，保留这里的资料
    const userId = user.id.toString()
    set({ userId, user, ended: null })
    tokens.setSession(userId, token)
  },

  signOut: async () => {
    try {
      await api.send(logout, {})
    } catch {
      // 请求失败也清除本地登录状态；服务端的会话会随刷新令牌过期
    }
    tokens.end("logout")
    // 退出请求可能因令牌失效先结束了登录状态（原因为 expired），用户主动退出时不再提示
    set({ ended: "logout" })
  },

  loadMe: async () => {
    const res = await api.send(getMe, {})
    if (res.user && res.user.id.toString() === get().userId) set({ user: res.user })
  },

  dismissEnded: () => set({ ended: null }),
}))

// 界面上展示的当前用户。资料还没加载或加载失败时名称显示「用户 <ID>」（设计 3.6.6）
export function useMe(): { id: string; nickname: string; email: string; avatarId?: string } | null {
  const userId = useSession((s) => s.userId)
  const user = useSession((s) => s.user)
  if (!userId) return null
  return { id: userId, nickname: user?.nickname || `用户 ${userId}`, email: user?.email ?? "", avatarId: user?.avatar || undefined }
}

tokens.subscribe((session, reason) => {
  if (!session) {
    useSession.setState({ signedIn: false, userId: null, user: null, ended: reason ?? "logout" })
    return
  }
  // 刷新令牌或其他标签页登录。换了用户时清掉旧资料，由 loadMe 重新加载
  const { userId, user } = useSession.getState()
  useSession.setState({
    signedIn: true,
    userId: session.userId,
    user: userId === session.userId ? user : null,
    ended: null,
  })
})
