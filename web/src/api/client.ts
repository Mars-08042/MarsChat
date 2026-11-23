// 浏览器中的单例：localStorage、Web Locks、fetch 都在这里接入。
// 业务代码从这里取客户端；单元测试直接用 createHttp、createTokenStore 并注入假依赖。

import { deviceInfo } from "./device"
import { getDownloadUrls, refreshToken } from "./endpoints"
import { ApiError, UNKNOWN } from "./errors"
import { createDownloadUrlCache, createUploader, toRelativeUrl } from "./files"
import { createHttp } from "./http"
import { AUTH_KEY, createTokenStore } from "./tokens"

const REFRESH_LOCK = "marschat.auth.refresh"

export const api = createHttp({ fetch: (input, init) => fetch(input, init), tokens: () => tokens })

export const tokens = createTokenStore({
  storage: localStorage,
  // Web Locks 只在安全上下文可用（localhost 与 HTTPS），其他情况退化为不加锁
  withLock: (fn) => (navigator.locks ? navigator.locks.request(REFRESH_LOCK, () => fn()) : fn()),
  refresh: async (token) => {
    const res = await api.send(refreshToken, { refreshToken: token })
    if (!res.token) throw new ApiError(500, UNKNOWN, "refresh response without token")
    return res.token
  },
})

export const currentDevice = () => deviceInfo(localStorage, navigator.userAgent)

export const uploadFile = createUploader({ createXhr: () => new XMLHttpRequest(), tokens: () => tokens })

export const downloadUrls = createDownloadUrlCache({
  fetch: async (items) => (await api.send(getDownloadUrls, { items })).urls,
  rewrite: toRelativeUrl,
})

// 其他标签页登录、退出或刷新令牌后同步；key 为 null 表示 localStorage 被整体清空
window.addEventListener("storage", (e) => {
  if (e.key === AUTH_KEY || e.key === null) tokens.syncFromStorage()
})
