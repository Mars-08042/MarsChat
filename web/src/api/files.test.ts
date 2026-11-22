import { describe, expect, it, vi } from "vitest"
import { create } from "@bufbuild/protobuf"
import { timestampFromMs } from "@bufbuild/protobuf/wkt"
import { DownloadUrlSchema, FileKind, Purpose, Variant } from "@/gen/media/v1/media_pb"
import { ApiError, uploadErrorText } from "./errors"
import { createDownloadUrlCache, createUploader, isUploadAborted, MAX_BATCH, REUSE_MARGIN_MS, toProxiedUrl, type DownloadUrlItem } from "./files"
import type { TokenStore } from "./tokens"

// ---------- 上传 ----------

// 只实现上传用到的部分：记录请求头，测试里手动触发进度、完成、出错
class FakeXhr {
  static all: FakeXhr[] = []
  method = ""
  url = ""
  headers: Record<string, string> = {}
  body: unknown
  status = 0
  responseText = ""
  aborted = false
  upload: { onprogress: ((e: { lengthComputable: boolean; loaded: number; total: number }) => void) | null } = { onprogress: null }
  onload: (() => void) | null = null
  onerror: (() => void) | null = null
  ontimeout: (() => void) | null = null
  onabort: (() => void) | null = null

  constructor() {
    FakeXhr.all.push(this)
  }
  open(method: string, url: string) {
    this.method = method
    this.url = url
  }
  setRequestHeader(k: string, v: string) {
    this.headers[k] = v
  }
  send(body: unknown) {
    this.body = body
  }
  abort() {
    this.aborted = true
    this.onabort?.()
  }
  respond(status: number, body: unknown) {
    this.status = status
    this.responseText = typeof body === "string" ? body : JSON.stringify(body)
    this.onload?.()
  }
}

const fileInfoJson = { id: "42", accessKey: "key", purpose: "PURPOSE_CHAT", kind: "FILE_KIND_IMAGE", name: "a.png", size: "1024", mimeType: "image/png", width: 800, height: 600, hasThumbnail: true }

function setup(token = "t1") {
  FakeXhr.all = []
  const tokens = {
    accessToken: vi.fn(async () => token),
    refresh: vi.fn(async () => "t2"),
    end: vi.fn(),
  } as unknown as TokenStore
  const upload = createUploader({ createXhr: () => new FakeXhr() as unknown as XMLHttpRequest, tokens: () => tokens })
  return { upload, tokens }
}

// 等 accessToken 的 Promise 结算，XHR 才会创建
const tick = () => new Promise((r) => setTimeout(r, 0))

describe("createUploader", () => {
  it("请求体是文件本身，参数在查询串里，返回服务端的文件信息并报告进度", async () => {
    const { upload } = setup()
    const progress = vi.fn()
    const blob = new Blob(["hello"])
    const task = upload(blob, { name: "照片 1.png", purpose: Purpose.CHAT, onProgress: progress })
    await tick()
    const x = FakeXhr.all[0]
    expect(x.method).toBe("POST")
    expect(x.url).toBe(`/v1/files?purpose=PURPOSE_CHAT&name=${encodeURIComponent("照片 1.png")}`)
    expect(x.headers.Authorization).toBe("Bearer t1")
    expect(x.body).toBe(blob)
    x.upload.onprogress?.({ lengthComputable: true, loaded: 2, total: 5 })
    expect(progress).toHaveBeenCalledWith(2, 5)
    x.respond(200, fileInfoJson)
    const info = await task.promise
    expect(info.id).toBe(42n)
    expect(info.kind).toBe(FileKind.IMAGE)
    expect(info.hasThumbnail).toBe(true)
  })

  it("头像用 PURPOSE_AVATAR", async () => {
    const { upload } = setup()
    void upload(new Blob(["x"]), { name: "a.png", purpose: Purpose.AVATAR }).promise.catch(() => undefined)
    await tick()
    expect(FakeXhr.all[0].url).toContain("purpose=PURPOSE_AVATAR")
  })

  it("401 时刷新令牌重试一次；仍是 401 就结束登录状态", async () => {
    const { upload, tokens } = setup()
    const task = upload(new Blob(["x"]), { name: "a", purpose: Purpose.CHAT })
    await tick()
    FakeXhr.all[0].respond(401, { code: 401, reason: "UNAUTHORIZED" })
    await tick()
    expect(tokens.refresh).toHaveBeenCalledWith("t1")
    expect(FakeXhr.all[1].headers.Authorization).toBe("Bearer t2")
    FakeXhr.all[1].respond(401, { code: 401, reason: "UNAUTHORIZED" })
    await expect(task.promise).rejects.toMatchObject({ reason: "UNAUTHORIZED" })
    expect(tokens.end).toHaveBeenCalledWith("expired")
  })

  it("服务端错误解析为 ApiError，文案带上限", async () => {
    const { upload } = setup()
    const task = upload(new Blob(["x"]), { name: "a", purpose: Purpose.CHAT })
    await tick()
    FakeXhr.all[0].respond(413, { code: 413, reason: "FILE_TOO_LARGE", metadata: { max_bytes: String(100 * 1024 * 1024) } })
    const e = await task.promise.catch((err: unknown) => err)
    expect(e).toBeInstanceOf(ApiError)
    expect(uploadErrorText(e)).toBe("文件超过 100 MB 上限")
  })

  it("网络中断和限流的文案按上传场景给出", async () => {
    const { upload } = setup()
    const task = upload(new Blob(["x"]), { name: "a", purpose: Purpose.CHAT })
    await tick()
    FakeXhr.all[0].onerror?.()
    expect(uploadErrorText(await task.promise.catch((e: unknown) => e))).toBe("网络中断，文件没有传完")
    expect(uploadErrorText(new ApiError(429, "RATE_LIMITED", "", { retry_after_seconds: "120" }))).toBe("上传太频繁，请 2 分钟后再试")
    expect(uploadErrorText(new ApiError(400, "UPLOAD_INCOMPLETE", ""))).toBe("网络中断，文件没有传完")
    expect(uploadErrorText(new ApiError(503, "DEPENDENCY_UNAVAILABLE", ""))).toBe("文件服务暂时不可用，请稍后重试")
  })

  it("取消时中止请求，结果标为取消", async () => {
    const { upload } = setup()
    const task = upload(new Blob(["x"]), { name: "a", purpose: Purpose.CHAT })
    await tick()
    task.abort()
    expect(FakeXhr.all[0].aborted).toBe(true)
    expect(isUploadAborted(await task.promise.catch((e: unknown) => e))).toBe(true)
  })

  it("取令牌期间取消，不再发出请求", async () => {
    const { upload } = setup()
    const task = upload(new Blob(["x"]), { name: "a", purpose: Purpose.CHAT })
    task.abort()
    expect(isUploadAborted(await task.promise.catch((e: unknown) => e))).toBe(true)
    expect(FakeXhr.all).toHaveLength(0)
  })
})

// ---------- 下载地址 ----------

const NOW = 1_000_000_000_000
const TTL = 10 * 60_000

function cacheSetup(omit: (item: DownloadUrlItem) => boolean = () => false) {
  let now = NOW
  const calls: DownloadUrlItem[][] = []
  const fetch = vi.fn(async (items: DownloadUrlItem[]) => {
    calls.push(items)
    return items
      .filter((i) => !omit(i))
      .map((i) =>
        create(DownloadUrlSchema, {
          fileId: i.fileId,
          variant: i.variant,
          url: `https://127.0.0.1:19443/v1/files/${i.fileId}/content?variant=${i.variant}&n=${calls.length}`,
          expiresAt: timestampFromMs(now + TTL),
        }),
      )
  })
  let pending: (() => void) | undefined
  const cache = createDownloadUrlCache({ fetch, now: () => now, schedule: (f) => (pending = f) })
  const flush = async () => {
    pending?.()
    pending = undefined
    await tick()
  }
  return { cache, fetch, calls, flush, advance: (ms: number) => (now += ms) }
}

describe("createDownloadUrlCache", () => {
  it("同一轮的请求合并成一次，同一个文件和变体只请求一次", async () => {
    const { cache, calls, flush } = cacheSetup()
    const a = cache.get("1", Variant.THUMBNAIL, "k1")
    const b = cache.get("2", Variant.THUMBNAIL, "k2")
    const a2 = cache.get("1", Variant.THUMBNAIL, "k1")
    await flush()
    expect(calls).toHaveLength(1)
    expect(calls[0]).toEqual([
      { fileId: 1n, accessKey: "k1", variant: Variant.THUMBNAIL },
      { fileId: 2n, accessKey: "k2", variant: Variant.THUMBNAIL },
    ])
    expect(await a).toContain("/v1/files/1/content")
    expect(await a2).toBe(await a)
    expect(await b).toContain("/v1/files/2/content")
  })

  it("剩余时间超过 1 分钟时复用，不足 1 分钟时重新换取", async () => {
    const { cache, calls, flush, advance } = cacheSetup()
    const first = cache.get("1", Variant.ORIGINAL)
    await flush()
    const url = await first
    advance(TTL - REUSE_MARGIN_MS - 1)
    expect(cache.peek("1", Variant.ORIGINAL)).toBe(url)
    expect(await cache.get("1", Variant.ORIGINAL)).toBe(url)
    expect(calls).toHaveLength(1)
    advance(2)
    expect(cache.peek("1", Variant.ORIGINAL)).toBeUndefined()
    const again = cache.get("1", Variant.ORIGINAL)
    await flush()
    expect(calls).toHaveLength(2)
    expect(await again).not.toBe(url)
  })

  it("接口省略的项记为 null，之后不再请求", async () => {
    const { cache, calls, flush } = cacheSetup((i) => i.fileId === 9n)
    const missing = cache.get("9", Variant.THUMBNAIL)
    await flush()
    expect(await missing).toBeNull()
    expect(cache.peek("9", Variant.THUMBNAIL)).toBeNull()
    expect(await cache.get("9", Variant.THUMBNAIL)).toBeNull()
    expect(calls).toHaveLength(1)
  })

  it("超过 100 项时分批请求", async () => {
    const { cache, calls, flush } = cacheSetup()
    const all = Array.from({ length: MAX_BATCH + 5 }, (_, i) => cache.get(String(i + 1), Variant.THUMBNAIL))
    await flush()
    expect(calls.map((c) => c.length)).toEqual([MAX_BATCH, 5])
    expect((await Promise.all(all)).every((u) => u !== null)).toBe(true)
  })

  it("invalidate 后重新换取；clear 之后到达的旧响应不写入缓存", async () => {
    const { cache, calls, flush } = cacheSetup()
    const first = cache.get("1", Variant.THUMBNAIL)
    await flush()
    await first
    cache.invalidate("1", Variant.THUMBNAIL)
    expect(cache.peek("1", Variant.THUMBNAIL)).toBeUndefined()

    const pending = cache.get("1", Variant.THUMBNAIL)
    cache.clear()
    await flush()
    await pending
    expect(calls).toHaveLength(2)
    expect(cache.peek("1", Variant.THUMBNAIL)).toBeUndefined()
  })

  it("请求失败时所有等待者都收到错误，下次重新请求", async () => {
    const { cache, fetch, flush } = cacheSetup()
    fetch.mockRejectedValueOnce(new ApiError(0, "NETWORK", "down"))
    // 先挂上处理函数，避免在 flush 期间被当成未处理的拒绝
    const failed = cache.get("1", Variant.THUMBNAIL).catch((e: unknown) => e)
    await flush()
    expect(await failed).toMatchObject({ reason: "NETWORK" })
    const retry = cache.get("1", Variant.THUMBNAIL)
    await flush()
    expect(await retry).toContain("/v1/files/1/content")
  })
})

describe("toProxiedUrl", () => {
  it("本地存储的下载地址改成相对路径，其他地址不变", () => {
    expect(toProxiedUrl("https://127.0.0.1:19443/v1/files/12/content?variant=2&expires=1&signature=abc")).toBe("/v1/files/12/content?variant=2&expires=1&signature=abc")
    expect(toProxiedUrl("http://127.0.0.1:18050/v1/files/12/content?variant=1")).toBe("/v1/files/12/content?variant=1")
    const oss = "https://bucket.oss-cn-hangzhou.aliyuncs.com/marschat/12?Expires=1&Signature=x"
    expect(toProxiedUrl(oss)).toBe(oss)
    expect(toProxiedUrl("not a url")).toBe("not a url")
  })
})
