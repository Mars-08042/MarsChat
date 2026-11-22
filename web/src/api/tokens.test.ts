import { describe, expect, it, vi } from "vitest"
import { ApiError, NETWORK } from "./errors"
import { createLock, grant, memoryStorage } from "./test-utils"
import { AUTH_KEY, createTokenStore, type Session, type TokenGrant, type TokenStoreDeps } from "./tokens"

const T0 = 1_800_000_000_000
const GRACE_MS = 10_000

// 模拟 account 的刷新规则：每次刷新轮换刷新令牌；宽限期内重复使用旧令牌返回 REFRESH_TOKEN_ROTATED，
// 超过宽限期后重放视为泄露，吊销整个会话
function fakeServer(now: () => number) {
  let n = 1
  let current = "r1"
  let previous: { token: string; at: number } | null = null
  let revoked = false
  const calls: string[] = []

  const refresh = async (token: string): Promise<TokenGrant> => {
    calls.push(token)
    // 让出一次事件循环，模拟网络往返，便于并发请求交错
    await new Promise((r) => setTimeout(r, 0))
    if (revoked) throw new ApiError(401, "REFRESH_TOKEN_INVALID", "")
    if (token === current) {
      previous = { token, at: now() }
      n++
      current = `r${n}`
      return grant(n)
    }
    if (previous?.token === token && now() - previous.at <= GRACE_MS) throw new ApiError(409, "REFRESH_TOKEN_ROTATED", "")
    revoked = true
    throw new ApiError(401, "REFRESH_TOKEN_INVALID", "")
  }
  return { refresh, calls, isRevoked: () => revoked }
}

function stored(storage: ReturnType<typeof memoryStorage>): Session | null {
  const raw = storage.getItem(AUTH_KEY)
  return raw ? (JSON.parse(raw) as Session) : null
}

// 一个浏览器：共用 localStorage、锁、时钟和后端，tab() 打开一个新标签页
function browser(opts: { refresh?: TokenStoreDeps["refresh"] } = {}) {
  let t = T0
  const now = () => t
  const storage = memoryStorage()
  const withLock = createLock()
  const server = fakeServer(now)
  const tab = () => createTokenStore({ storage, withLock, refresh: opts.refresh ?? server.refresh, now })
  const advance = (ms: number) => {
    t += ms
  }
  return { storage, server, tab, advance, now }
}

describe("登录与读取", () => {
  it("setSession 写入 localStorage 并通知，新标签页读到同一个会话", () => {
    const b = browser()
    const a = b.tab()
    const listener = vi.fn()
    a.subscribe(listener)
    a.setSession("42", grant(1))

    const want: Session = {
      userId: "42",
      accessToken: "a1",
      accessExpiresAt: T0 + 900_000,
      refreshToken: "r1",
      refreshExpiresAt: T0 + 30 * 86400_000,
    }
    expect(stored(b.storage)).toEqual(want)
    expect(listener).toHaveBeenCalledWith(want, undefined)
    expect(b.tab().current()).toEqual(want)
  })

  it("localStorage 内容损坏或缺字段时视为未登录", () => {
    const b = browser()
    b.storage.setItem(AUTH_KEY, "{")
    expect(b.tab().current()).toBeNull()
    b.storage.setItem(AUTH_KEY, JSON.stringify({ userId: "1", accessToken: "a" }))
    expect(b.tab().current()).toBeNull()
  })

  it("未登录时取访问令牌返回 401", async () => {
    await expect(browser().tab().accessToken()).rejects.toMatchObject({ status: 401, reason: "UNAUTHORIZED" })
  })
})

describe("刷新时机", () => {
  it("剩余有效期多于 60 秒时直接使用，不刷新", async () => {
    const b = browser()
    const a = b.tab()
    a.setSession("42", grant(1))
    b.advance(900_000 - 61_000)
    expect(await a.accessToken()).toBe("a1")
    expect(b.server.calls).toEqual([])
  })

  it("剩余 60 秒以内时先刷新，新令牌写回 localStorage", async () => {
    const b = browser()
    const a = b.tab()
    a.setSession("42", grant(1))
    b.advance(900_000 - 60_000)
    expect(await a.accessToken()).toBe("a2")
    expect(b.server.calls).toEqual(["r1"])
    expect(stored(b.storage)).toMatchObject({ userId: "42", accessToken: "a2", refreshToken: "r2" })
  })

  it("同一标签页内的并发刷新合并为一次", async () => {
    const b = browser()
    const a = b.tab()
    a.setSession("42", grant(1))
    b.advance(900_000)
    const got = await Promise.all([a.accessToken(), a.accessToken(), a.refresh("a1")])
    expect(got).toEqual(["a2", "a2", "a2"])
    expect(b.server.calls).toEqual(["r1"])
  })
})

describe("多标签页", () => {
  it("多个标签页同时刷新：只消耗一次刷新令牌，会话不被吊销", async () => {
    const b = browser()
    const tabs = [b.tab(), b.tab(), b.tab()]
    tabs[0].setSession("42", grant(1))
    tabs.slice(1).forEach((t) => t.syncFromStorage())
    b.advance(900_000)

    const got = await Promise.all(tabs.map((t) => t.accessToken()))
    expect(got).toEqual(["a2", "a2", "a2"])
    expect(b.server.calls).toEqual(["r1"])
    expect(b.server.isRevoked()).toBe(false)
    tabs.forEach((t) => expect(t.current()?.refreshToken).toBe("r2"))
  })

  it("拿到锁时别的标签页已经刷新过，直接使用新令牌", async () => {
    const b = browser()
    const a = b.tab()
    const other = b.tab()
    a.setSession("42", grant(1))
    other.syncFromStorage()
    b.advance(900_000)
    expect(await other.accessToken()).toBe("a2")

    // a 还没收到 storage 事件，仍以为自己拿的是 a1
    expect(a.current()?.accessToken).toBe("a1")
    expect(await a.refresh("a1")).toBe("a2")
    expect(b.server.calls).toEqual(["r1"])
  })

  it("没有锁时并发刷新收到 REFRESH_TOKEN_ROTATED，改用 localStorage 中最新的令牌", async () => {
    // 两个标签页各用一把锁，模拟不支持 Web Locks 的环境
    let t = T0
    const now = () => t
    const storage = memoryStorage()
    const server = fakeServer(now)
    const mk = () => createTokenStore({ storage, withLock: createLock(), refresh: server.refresh, now })
    const a = mk()
    const c = mk()
    a.setSession("42", grant(1))
    c.syncFromStorage()
    t += 900_000

    const got = await Promise.all([a.accessToken(), c.accessToken()])
    expect(got).toEqual(["a2", "a2"])
    expect(server.calls).toEqual(["r1", "r1"])
    expect(server.isRevoked()).toBe(false)
  })

  it("其他标签页退出：通知原因为 logout", () => {
    const b = browser()
    const a = b.tab()
    const other = b.tab()
    a.setSession("42", grant(1))
    other.syncFromStorage()
    const listener = vi.fn()
    a.subscribe(listener)

    other.end("logout")
    a.syncFromStorage()
    expect(a.current()).toBeNull()
    expect(listener).toHaveBeenCalledExactlyOnceWith(null, "logout")
  })

  it("其他标签页换了账号登录：同步新会话", () => {
    const b = browser()
    const a = b.tab()
    const other = b.tab()
    a.setSession("42", grant(1))
    const listener = vi.fn()
    a.subscribe(listener)

    other.setSession("7", grant(9))
    a.syncFromStorage()
    expect(a.current()).toMatchObject({ userId: "7", accessToken: "a9" })
    expect(listener).toHaveBeenCalledExactlyOnceWith(expect.objectContaining({ userId: "7" }), undefined)
  })

  it("localStorage 没有变化时不通知", () => {
    const b = browser()
    const a = b.tab()
    a.setSession("42", grant(1))
    const listener = vi.fn()
    a.subscribe(listener)
    a.syncFromStorage()
    expect(listener).not.toHaveBeenCalled()
  })
})

describe("刷新失败", () => {
  const signedInTab = (refresh: TokenStoreDeps["refresh"]) => {
    const b = browser({ refresh })
    const a = b.tab()
    a.setSession("42", grant(1))
    const listener = vi.fn()
    a.subscribe(listener)
    b.advance(900_000)
    return { b, a, listener }
  }

  it("刷新令牌无效：结束登录状态，原因为 expired", async () => {
    const { b, a, listener } = signedInTab(() => Promise.reject(new ApiError(401, "REFRESH_TOKEN_INVALID", "")))
    await expect(a.accessToken()).rejects.toMatchObject({ reason: "REFRESH_TOKEN_INVALID" })
    expect(a.current()).toBeNull()
    expect(stored(b.storage)).toBeNull()
    expect(listener).toHaveBeenCalledExactlyOnceWith(null, "expired")
  })

  it("收到 REFRESH_TOKEN_ROTATED 但本地没有更新的令牌：按过期处理", async () => {
    const { a, listener } = signedInTab(() => Promise.reject(new ApiError(409, "REFRESH_TOKEN_ROTATED", "")))
    await expect(a.accessToken()).rejects.toMatchObject({ reason: "REFRESH_TOKEN_ROTATED" })
    expect(listener).toHaveBeenCalledExactlyOnceWith(null, "expired")
  })

  it("用户被封禁：原因为 banned", async () => {
    const { a, listener } = signedInTab(() => Promise.reject(new ApiError(403, "USER_BANNED", "")))
    await expect(a.accessToken()).rejects.toMatchObject({ reason: "USER_BANNED" })
    expect(listener).toHaveBeenCalledExactlyOnceWith(null, "banned")
  })

  it.each([
    ["网络错误", new ApiError(0, NETWORK, "Failed to fetch")],
    ["服务不可用", new ApiError(503, "DEPENDENCY_UNAVAILABLE", "")],
    ["网关限流", new ApiError(429, "GATEWAY_RATE_LIMITED", "")],
  ])("%s：保留登录状态，下次再试", async (_, err) => {
    let fail = true
    const { a, listener } = signedInTab(async () => {
      if (fail) throw err
      return grant(2)
    })
    await expect(a.accessToken()).rejects.toBe(err)
    expect(a.current()?.refreshToken).toBe("r1")
    expect(listener).not.toHaveBeenCalled()

    fail = false
    expect(await a.accessToken()).toBe("a2")
  })

  it("刷新令牌在本地已过期：不请求后端，直接结束", async () => {
    const refresh = vi.fn(() => Promise.resolve(grant(2)))
    const { b, a, listener } = signedInTab(refresh)
    b.advance(30 * 86400_000)
    await expect(a.accessToken()).rejects.toMatchObject({ status: 401 })
    expect(refresh).not.toHaveBeenCalled()
    expect(listener).toHaveBeenCalledExactlyOnceWith(null, "expired")
  })

  it("刷新前其他标签页已经退出：不再刷新", async () => {
    const refresh = vi.fn(() => Promise.resolve(grant(2)))
    const { b, a, listener } = signedInTab(refresh)
    b.storage.removeItem(AUTH_KEY)
    await expect(a.accessToken()).rejects.toMatchObject({ status: 401, reason: "UNAUTHORIZED" })
    expect(refresh).not.toHaveBeenCalled()
    expect(listener).toHaveBeenCalledExactlyOnceWith(null, "logout")
  })

  it("重复结束只通知一次", () => {
    const b = browser()
    const a = b.tab()
    a.setSession("42", grant(1))
    const listener = vi.fn()
    a.subscribe(listener)
    a.end("expired")
    a.end("logout")
    expect(listener).toHaveBeenCalledExactlyOnceWith(null, "expired")
  })

  it("取消订阅后不再通知", () => {
    const a = browser().tab()
    const listener = vi.fn()
    const off = a.subscribe(listener)
    off()
    a.setSession("42", grant(1))
    expect(listener).not.toHaveBeenCalled()
  })
})
