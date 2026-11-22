import { describe, expect, it, vi } from "vitest"
import { Platform } from "@/gen/account/v1/account_pb"
import { DifferenceResult } from "@/gen/message/v1/message_pb"
import { getDifference, getMe, listConversations, loginByPassword, logout, markRead } from "./endpoints"
import { NETWORK } from "./errors"
import { createHttp } from "./http"
import type { TokenStore } from "./tokens"

const json = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } })

const unauthorized = () => json(401, { code: 401, reason: "UNAUTHORIZED", message: "token expired" })

function fakeTokens(access = "a1", refreshed = "a2") {
  return {
    accessToken: vi.fn(() => Promise.resolve(access)),
    refresh: vi.fn(() => Promise.resolve(refreshed)),
    end: vi.fn(),
  }
}

function setup(responses: Array<Response | Error>, tokens = fakeTokens()) {
  const fetch = vi.fn<typeof globalThis.fetch>(async () => {
    const next = responses.shift()
    if (!next) throw new Error("unexpected request")
    if (next instanceof Error) throw next
    return next
  })
  const http = createHttp({ fetch, tokens: () => tokens as unknown as TokenStore })
  const headers = (i: number) => fetch.mock.calls[i][1]?.headers as Record<string, string>
  return { http, fetch, tokens, headers }
}

describe("请求格式", () => {
  it("POST：protobuf JSON 请求体（lowerCamelCase、枚举用名称），免登录接口不带令牌", async () => {
    const { http, fetch, tokens, headers } = setup([
      json(200, {
        user: { id: "1234567890123", email: "a@example.com", nickname: "阿星", role: "ROLE_USER", futureField: 1 },
        token: { accessToken: "a1", accessExpiresIn: 900, refreshToken: "r1", refreshExpiresIn: 2592000 },
      }),
    ])
    const res = await http.send(loginByPassword, {
      email: "a@example.com",
      password: "secret123",
      device: { deviceId: "d".repeat(32), platform: Platform.WEB, deviceName: "Chrome · Windows" },
    })

    const [url, init] = fetch.mock.calls[0]
    expect(url).toBe("/v1/auth/login/password")
    expect(init?.method).toBe("POST")
    expect(JSON.parse(init?.body as string)).toEqual({
      email: "a@example.com",
      password: "secret123",
      device: { deviceId: "d".repeat(32), platform: "PLATFORM_WEB", deviceName: "Chrome · Windows" },
    })
    expect(headers(0)["Content-Type"]).toBe("application/json")
    expect(headers(0).Authorization).toBeUndefined()
    expect(tokens.accessToken).not.toHaveBeenCalled()

    // int64 解析为 bigint，未知字段忽略
    expect(res.user?.id).toBe(1234567890123n)
    expect(res.user?.nickname).toBe("阿星")
    expect(res.token?.refreshExpiresIn).toBe(2592000)
  })

  it("GET：没有请求体，需要登录的接口带访问令牌", async () => {
    const { http, fetch, headers } = setup([json(200, { user: { id: "42", nickname: "林晚" } })])
    const res = await http.send(getMe, {})
    const init = fetch.mock.calls[0][1]
    expect(init?.method).toBe("GET")
    expect(init?.body).toBeUndefined()
    expect(headers(0)["Content-Type"]).toBeUndefined()
    expect(headers(0).Authorization).toBe("Bearer a1")
    expect(res.user?.id).toBe(42n)
  })

  it("GET：路径参数取同名字段，其余字段放进查询串，默认值不出现", async () => {
    const { http, fetch } = setup([
      json(200, { result: "DIFFERENCE_RESULT_COMPLETE", maxSeq: "9" }),
      json(200, { conversations: [] }),
    ])
    const res = await http.send(getDifference, { conversationId: 117696723410747392n, fromSeq: 5n, limit: 200 })
    expect(fetch.mock.calls[0][0]).toBe("/v1/conversations/117696723410747392/difference?fromSeq=5&limit=200")
    expect(fetch.mock.calls[0][1]?.body).toBeUndefined()
    expect(res.result).toBe(DifferenceResult.COMPLETE)
    expect(res.maxSeq).toBe(9n)

    await http.send(listConversations, { cursor: 0n, limit: 100 })
    expect(fetch.mock.calls[1][0]).toBe("/v1/conversations?limit=100")
  })

  it("POST：路径参数同时保留在请求体里", async () => {
    const { http, fetch } = setup([json(200, { readSeq: "7" })])
    await http.send(markRead, { conversationId: 42n, seq: 7n })
    expect(fetch.mock.calls[0][0]).toBe("/v1/conversations/42/read")
    expect(JSON.parse(fetch.mock.calls[0][1]?.body as string)).toEqual({ conversationId: "42", seq: "7" })
  })

  it("缺少路径参数时不发请求", async () => {
    const { http, fetch } = setup([])
    await expect(http.send(getDifference, { fromSeq: 1n })).rejects.toThrow("missing path parameter conversation_id")
    expect(fetch).not.toHaveBeenCalled()
  })

  it("空响应体按空消息解析", async () => {
    const { http } = setup([new Response("", { status: 200 })])
    await expect(http.send(logout, {})).resolves.toBeDefined()
  })
})

describe("401 处理", () => {
  it("刷新令牌后用新令牌重试一次", async () => {
    const { http, fetch, tokens, headers } = setup([unauthorized(), json(200, { user: { id: "42" } })])
    const res = await http.send(getMe, {})
    expect(tokens.refresh).toHaveBeenCalledExactlyOnceWith("a1")
    expect(fetch).toHaveBeenCalledTimes(2)
    expect(headers(1).Authorization).toBe("Bearer a2")
    expect(res.user?.id).toBe(42n)
    expect(tokens.end).not.toHaveBeenCalled()
  })

  it("新令牌仍被拒绝：结束登录状态，不再重试", async () => {
    const { http, fetch, tokens } = setup([unauthorized(), unauthorized()])
    await expect(http.send(getMe, {})).rejects.toMatchObject({ status: 401, reason: "UNAUTHORIZED" })
    expect(fetch).toHaveBeenCalledTimes(2)
    expect(tokens.end).toHaveBeenCalledExactlyOnceWith("expired")
  })

  it("刷新失败时把刷新的错误抛给调用方", async () => {
    const tokens = fakeTokens()
    const err = new Error("refresh failed")
    tokens.refresh.mockRejectedValueOnce(err)
    const { http, fetch } = setup([unauthorized()], tokens)
    await expect(http.send(getMe, {})).rejects.toBe(err)
    expect(fetch).toHaveBeenCalledTimes(1)
  })

  it("免登录接口的 401（例如密码错误）不刷新令牌", async () => {
    const { http, tokens } = setup([json(401, { code: 401, reason: "INVALID_CREDENTIALS", message: "" })])
    await expect(
      http.send(loginByPassword, {
        email: "a@example.com",
        password: "wrong",
        device: { deviceId: "d".repeat(32), platform: Platform.WEB },
      }),
    ).rejects.toMatchObject({ status: 401, reason: "INVALID_CREDENTIALS" })
    expect(tokens.refresh).not.toHaveBeenCalled()
    expect(tokens.end).not.toHaveBeenCalled()
  })
})

describe("错误", () => {
  it("业务错误解析为 ApiError", async () => {
    const { http } = setup([
      json(429, { code: 429, reason: "LOGIN_LOCKED", message: "", metadata: { retry_after_seconds: "300" } }),
    ])
    await expect(
      http.send(loginByPassword, { email: "a@example.com", password: "x", device: { deviceId: "d".repeat(32), platform: Platform.WEB } }),
    ).rejects.toMatchObject({ status: 429, reason: "LOGIN_LOCKED", retryAfterSeconds: 300 })
  })

  it("请求没有发出去：状态码 0，原因为 NETWORK", async () => {
    const { http } = setup([new TypeError("Failed to fetch")])
    await expect(http.send(getMe, {})).rejects.toMatchObject({ status: 0, reason: NETWORK, message: "Failed to fetch" })
  })
})
