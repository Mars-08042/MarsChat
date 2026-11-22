// 文件上传与下载地址（设计 3.6.9，media-service.md）。浏览器里的单例在 api/client.ts，
// 这里只放工厂函数，单元测试注入假的 XMLHttpRequest 和下载地址接口。

import { fromJson } from "@bufbuild/protobuf"
import { timestampMs } from "@bufbuild/protobuf/wkt"
import { FileInfoSchema, Purpose, type DownloadUrl, type FileInfo, type Variant } from "@/gen/media/v1/media_pb"
import { ApiError, NETWORK, parseErrorResponse } from "./errors"
import type { TokenStore } from "./tokens"

// ---------- 上传 ----------

// 用户取消上传。调用方据此直接移除消息，不当作失败
export const UPLOAD_ABORTED = "UPLOAD_ABORTED"

export const isUploadAborted = (e: unknown) => e instanceof ApiError && e.reason === UPLOAD_ABORTED

export interface UploadOptions {
  name: string
  purpose: Purpose
  onProgress?: (loaded: number, total: number) => void
}

export interface UploadTask {
  promise: Promise<FileInfo>
  abort: () => void
}

export interface UploaderDeps {
  createXhr: () => XMLHttpRequest
  tokens: () => TokenStore
}

const PURPOSE_PARAM: Partial<Record<Purpose, string>> = {
  [Purpose.CHAT]: "PURPOSE_CHAT",
  [Purpose.AVATAR]: "PURPOSE_AVATAR",
}

// 上传接口的请求体是文件本身（POST /v1/files?purpose=&name=），fetch 拿不到上传进度，所以用 XMLHttpRequest。
// 浏览器按 Blob 自动带上 Content-Length。401 时与 api/http.ts 一样刷新令牌后重试一次
export function createUploader(deps: UploaderDeps): (file: Blob, options: UploadOptions) => UploadTask {
  return (file, { name, purpose, onProgress }) => {
    let xhr: XMLHttpRequest | undefined
    let aborted = false
    const abortedError = () => new ApiError(0, UPLOAD_ABORTED, "upload aborted")
    const url = `/v1/files?purpose=${PURPOSE_PARAM[purpose] ?? "PURPOSE_UNSPECIFIED"}&name=${encodeURIComponent(name)}`

    const send = (token: string) =>
      new Promise<{ status: number; body: string }>((resolve, reject) => {
        const x = deps.createXhr()
        xhr = x
        x.open("POST", url)
        x.setRequestHeader("Authorization", `Bearer ${token}`)
        x.setRequestHeader("Accept", "application/json")
        x.setRequestHeader("Content-Type", "application/octet-stream")
        x.upload.onprogress = (e) => {
          if (e.lengthComputable) onProgress?.(e.loaded, e.total)
        }
        x.onload = () => resolve({ status: x.status, body: x.responseText })
        x.onerror = () => reject(new ApiError(0, NETWORK, "upload network error"))
        x.ontimeout = () => reject(new ApiError(0, NETWORK, "upload timeout"))
        x.onabort = () => reject(abortedError())
        x.send(file)
      })

    const promise = (async () => {
      let token = await deps.tokens().accessToken()
      if (aborted) throw abortedError()
      let res = await send(token)
      if (res.status === 401) {
        token = await deps.tokens().refresh(token)
        if (aborted) throw abortedError()
        res = await send(token)
        // 刚换的令牌也被拒绝：会话已在服务端失效
        if (res.status === 401) deps.tokens().end("expired")
      }
      if (res.status < 200 || res.status >= 300) throw parseErrorResponse(res.status, res.body)
      return fromJson(FileInfoSchema, JSON.parse(res.body), { ignoreUnknownFields: true })
    })()

    return {
      promise,
      abort: () => {
        aborted = true
        xhr?.abort()
      },
    }
  }
}

// ---------- 下载地址 ----------

// 下载地址有效期 10 分钟；剩余不到 1 分钟的不再复用，避免用到半路过期
export const REUSE_MARGIN_MS = 60_000
// 接口每次最多 100 项
export const MAX_BATCH = 100
// 响应里没有过期时间时按 10 分钟算
const DEFAULT_TTL_MS = 10 * 60_000

export interface DownloadUrlItem {
  fileId: bigint
  accessKey: string
  variant: Variant
}

export interface DownloadUrlCacheDeps {
  fetch: (items: DownloadUrlItem[]) => Promise<DownloadUrl[]>
  // 改写服务端返回的地址（开发时改成相对路径走 Vite 代理，见 toProxiedUrl）
  rewrite?: (url: string) => string
  now?: () => number
  // 同一轮渲染里的请求合并成一次，默认在下一个宏任务发出
  schedule?: (flush: () => void) => void
}

export interface DownloadUrlCache {
  // 同步查缓存：还能复用的地址；null 表示无权访问或文件不存在；undefined 表示要重新换取
  peek(fileId: string, variant: Variant): string | null | undefined
  get(fileId: string, variant: Variant, accessKey?: string): Promise<string | null>
  // 地址失效（图片加载失败、签名过期）时丢掉缓存，下次 get 重新换取
  invalidate(fileId: string, variant: Variant): void
  // 退出登录、切换账号时清空
  clear(): void
}

// 接口省略的项（无权访问、不存在）记为 null。文件记录不会变化，之后也不会变得可以访问，所以一直缓存
type Entry = { url: string; expiresAt: number } | null

interface Waiter {
  key: string
  // 入队时的代数：之后 clear 过，结果就不写入缓存
  generation: number
  item: DownloadUrlItem
  resolve: (url: string | null) => void
  reject: (e: unknown) => void
}

const cacheKey = (fileId: string, variant: Variant) => `${fileId}:${variant}`

export function createDownloadUrlCache(deps: DownloadUrlCacheDeps): DownloadUrlCache {
  const now = deps.now ?? Date.now
  const rewrite = deps.rewrite ?? ((url: string) => url)
  const schedule = deps.schedule ?? ((flush: () => void) => setTimeout(flush, 0))
  const entries = new Map<string, Entry>()
  const inflight = new Map<string, Promise<string | null>>()
  let queue: Waiter[] = []
  let scheduled = false
  // 每次 clear 加一；clear 之前发起的请求，结果不写入缓存
  let generation = 0

  const peek = (fileId: string, variant: Variant) => {
    const e = entries.get(cacheKey(fileId, variant))
    if (e === null) return null
    if (e && e.expiresAt - now() > REUSE_MARGIN_MS) return e.url
    return undefined
  }

  const flush = () => {
    scheduled = false
    const batch = queue
    queue = []
    for (let i = 0; i < batch.length; i += MAX_BATCH) {
      const chunk = batch.slice(i, i + MAX_BATCH)
      deps.fetch(chunk.map((w) => w.item)).then(
        (urls) => {
          const found = new Map(urls.map((u) => [cacheKey(u.fileId.toString(), u.variant), u]))
          for (const w of chunk) {
            const u = found.get(w.key)
            const entry: Entry = u ? { url: rewrite(u.url), expiresAt: u.expiresAt ? timestampMs(u.expiresAt) : now() + DEFAULT_TTL_MS } : null
            if (w.generation === generation) {
              entries.set(w.key, entry)
              inflight.delete(w.key)
            }
            w.resolve(entry?.url ?? null)
          }
        },
        (e: unknown) => {
          for (const w of chunk) {
            if (w.generation === generation) inflight.delete(w.key)
            w.reject(e)
          }
        },
      )
    }
  }

  return {
    peek,
    get(fileId, variant, accessKey = "") {
      const cached = peek(fileId, variant)
      if (cached !== undefined) return Promise.resolve(cached)
      const key = cacheKey(fileId, variant)
      const pending = inflight.get(key)
      if (pending) return pending
      const promise = new Promise<string | null>((resolve, reject) => {
        queue.push({ key, generation, item: { fileId: BigInt(fileId), accessKey, variant }, resolve, reject })
      })
      inflight.set(key, promise)
      if (!scheduled) {
        scheduled = true
        schedule(flush)
      }
      return promise
    },
    invalidate(fileId, variant) {
      entries.delete(cacheKey(fileId, variant))
    },
    clear() {
      generation++
      entries.clear()
      inflight.clear()
    },
  }
}

// 开发时浏览器只访问 Vite 的源（设计 3.10）。容器模式下本地存储的下载地址指向网关的 HTTPS 端口，
// 证书是自签的，浏览器直接加载会失败；把 media 本地下载接口的地址改成相对路径，经 Vite 代理转发。
// OSS 的预签名地址路径不同，原样保留
export function toProxiedUrl(url: string): string {
  try {
    const u = new URL(url)
    return /^\/v1\/files\/\d+\/content$/.test(u.pathname) ? `${u.pathname}${u.search}` : url
  } catch {
    return url
  }
}
