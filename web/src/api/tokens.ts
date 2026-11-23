// 令牌管理（设计 3.8）：令牌对存在 localStorage，同一浏览器的所有标签页共享。
// - 访问令牌过期前 60 秒刷新；同一标签页内的并发刷新合并为一次；
// - 跨标签页用锁串行化，拿到锁后先看别的标签页是否已经刷新过；
// - 刷新令牌每次刷新都轮换，宽限期内的重复刷新返回 REFRESH_TOKEN_ROTATED，此时改用本地最新的令牌；
// - 刷新失败（令牌无效、会话已吊销、用户被封禁）时结束登录状态并给出原因。
// 存储、锁、刷新接口和时钟都由调用方注入，单元测试不依赖浏览器。

import { ApiError, isApiError } from "./errors"

export const AUTH_KEY = "marschat.auth"
// 访问令牌剩余有效期少于这个值时先刷新再使用
export const REFRESH_AHEAD_MS = 60_000

export type KeyValueStorage = Pick<Storage, "getItem" | "setItem" | "removeItem">

export interface Session {
  userId: string
  accessToken: string
  // 毫秒时间戳，由本地时钟加上有效期算出
  accessExpiresAt: number
  refreshToken: string
  refreshExpiresAt: number
}

// 后端 TokenPair 中用到的字段，有效期单位为秒
export interface TokenGrant {
  accessToken: string
  accessExpiresIn: number
  refreshToken: string
  refreshExpiresIn: number
}

// 登录状态结束的原因。logout 是本浏览器主动退出（包括在其他标签页退出），不显示失效提示；
// 其余显示失效遮罩：expired 刷新令牌失效，后四种来自长连接的 Kicked（data-flow 3.2）：
// banned 被封禁，kicked 在其他设备上被移出，replaced 同一设备重新登录，reuse 刷新令牌疑似泄露，revoked 心跳复查发现令牌已吊销
export type EndReason = "logout" | "expired" | "banned" | "kicked" | "replaced" | "reuse" | "revoked"

// session 为空表示登录状态已结束，reason 为结束原因
export type SessionListener = (session: Session | null, reason?: EndReason) => void

export interface TokenStoreDeps {
  storage: KeyValueStorage
  // 跨标签页串行执行 fn（浏览器中为 Web Locks）
  withLock: <T>(fn: () => Promise<T>) => Promise<T>
  // 用刷新令牌换新的令牌对，请求不带访问令牌
  refresh: (refreshToken: string) => Promise<TokenGrant>
  now?: () => number
}

export interface TokenStore {
  current(): Session | null
  setSession(userId: string, grant: TokenGrant): void
  end(reason: EndReason): void
  // 返回可用的访问令牌，快过期时先刷新
  accessToken(): Promise<string>
  // staleAccessToken 是被拒绝或快过期的令牌，用来判断别的标签页是否已经换过新令牌
  refresh(staleAccessToken: string): Promise<string>
  // 其他标签页修改了 localStorage（storage 事件）后调用
  syncFromStorage(): void
  subscribe(listener: SessionListener): () => void
}

function parseSession(raw: string | null): Session | null {
  if (!raw) return null
  try {
    const v = JSON.parse(raw) as Partial<Session> | null
    if (
      v &&
      typeof v.userId === "string" &&
      typeof v.accessToken === "string" &&
      typeof v.refreshToken === "string" &&
      typeof v.accessExpiresAt === "number" &&
      typeof v.refreshExpiresAt === "number"
    ) {
      const { userId, accessToken, accessExpiresAt, refreshToken, refreshExpiresAt } = v
      return { userId, accessToken, accessExpiresAt, refreshToken, refreshExpiresAt }
    }
  } catch {
    // 内容损坏按未登录处理
  }
  return null
}

const sameSession = (a: Session | null, b: Session | null) =>
  a?.userId === b?.userId && a?.accessToken === b?.accessToken && a?.refreshToken === b?.refreshToken

export function createTokenStore(deps: TokenStoreDeps): TokenStore {
  const now = deps.now ?? Date.now
  const listeners = new Set<SessionListener>()
  const read = () => parseSession(deps.storage.getItem(AUTH_KEY))
  let session = read()
  let inflight: Promise<string> | null = null

  const notify = (reason?: EndReason) => listeners.forEach((l) => l(session, reason))
  const fresh = (s: Session) => s.accessExpiresAt - now() > REFRESH_AHEAD_MS

  const adopt = (s: Session) => {
    if (sameSession(s, session)) return
    session = s
    notify()
  }

  const setSession = (userId: string, grant: TokenGrant) => {
    const t = now()
    session = {
      userId,
      accessToken: grant.accessToken,
      accessExpiresAt: t + grant.accessExpiresIn * 1000,
      refreshToken: grant.refreshToken,
      refreshExpiresAt: t + grant.refreshExpiresIn * 1000,
    }
    deps.storage.setItem(AUTH_KEY, JSON.stringify(session))
    notify()
  }

  const end = (reason: EndReason) => {
    const had = session !== null
    session = null
    deps.storage.removeItem(AUTH_KEY)
    // 已经结束过（例如并发请求先后失败）时不重复通知
    if (had) notify(reason)
  }

  const doRefresh = (stale: string) =>
    deps.withLock(async () => {
      const latest = read()
      if (!latest) {
        // 其他标签页已经退出
        end("logout")
        throw new ApiError(401, "UNAUTHORIZED", "signed out")
      }
      if (latest.accessToken !== stale && fresh(latest)) {
        // 其他标签页刚刷新过，直接使用它的结果，不再消耗一次刷新令牌
        adopt(latest)
        return latest.accessToken
      }
      if (latest.refreshExpiresAt <= now()) {
        end("expired")
        throw new ApiError(401, "REFRESH_TOKEN_INVALID", "refresh token expired")
      }
      try {
        const grant = await deps.refresh(latest.refreshToken)
        setSession(latest.userId, grant)
        return grant.accessToken
      } catch (e) {
        if (!isApiError(e)) throw e
        if (e.reason === "REFRESH_TOKEN_ROTATED") {
          // 这个刷新令牌刚被另一个请求轮换：本地若已有更新的令牌就用它，否则按过期处理
          const again = read()
          if (again && again.refreshToken !== latest.refreshToken) {
            adopt(again)
            return again.accessToken
          }
          end("expired")
          throw e
        }
        if (e.reason === "USER_BANNED") {
          end("banned")
          throw e
        }
        if (e.status === 401) {
          end("expired")
          throw e
        }
        // 网络错误、限流、服务暂时不可用：保留登录状态，下次请求再试
        throw e
      }
    })

  const refresh = (stale: string) => {
    inflight ??= doRefresh(stale).finally(() => {
      inflight = null
    })
    return inflight
  }

  return {
    current: () => session,
    setSession,
    end,
    accessToken: async () => {
      const s = session
      if (!s) throw new ApiError(401, "UNAUTHORIZED", "not signed in")
      return fresh(s) ? s.accessToken : refresh(s.accessToken)
    },
    refresh,
    syncFromStorage: () => {
      const latest = read()
      if (sameSession(latest, session)) return
      const had = session !== null
      session = latest
      if (latest) notify()
      else if (had) notify("logout")
    },
    subscribe: (listener) => {
      listeners.add(listener)
      return () => listeners.delete(listener)
    },
  }
}
