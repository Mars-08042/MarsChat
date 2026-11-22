// HTTP 客户端（设计 3.7）：相对路径，请求体与响应体都是 protobuf JSON，由生成代码的 toJson、fromJson 转换。
// 需要登录的接口带访问令牌；收到 401 时刷新令牌后重试一次，仍是 401 就结束登录状态。

import { create, fromJson, toJson, type DescMessage, type MessageInitShape, type MessageShape } from "@bufbuild/protobuf"
import { ApiError, NETWORK, parseErrorResponse } from "./errors"
import type { TokenStore } from "./tokens"

export type HttpMethod = "GET" | "POST" | "PUT" | "PATCH" | "DELETE"

export interface Endpoint<I extends DescMessage, O extends DescMessage> {
  method: HttpMethod
  path: string
  input: I
  output: O
  auth: boolean
}

export function endpoint<I extends DescMessage, O extends DescMessage>(
  method: HttpMethod,
  path: string,
  input: I,
  output: O,
  auth = true,
): Endpoint<I, O> {
  return { method, path, input, output, auth }
}

export interface HttpDeps {
  fetch: typeof fetch
  // 用函数取得，避免与令牌管理互相依赖时的初始化顺序问题（令牌刷新本身也经由这个客户端）
  tokens: () => TokenStore
}

export type Send = <I extends DescMessage, O extends DescMessage>(
  ep: Endpoint<I, O>,
  req: MessageInitShape<I>,
) => Promise<MessageShape<O>>

export function createHttp(deps: HttpDeps): { send: Send } {
  const send: Send = async (ep, req) => {
    // GET 接口目前都没有参数；以后有查询参数的列表接口再加
    const body = ep.method === "GET" ? undefined : JSON.stringify(toJson(ep.input, create(ep.input, req)))

    const request = async (token: string | undefined) => {
      const headers: Record<string, string> = { Accept: "application/json" }
      if (body !== undefined) headers["Content-Type"] = "application/json"
      if (token) headers.Authorization = `Bearer ${token}`
      try {
        return await deps.fetch(ep.path, { method: ep.method, headers, body })
      } catch (e) {
        throw new ApiError(0, NETWORK, e instanceof Error ? e.message : String(e))
      }
    }

    let token = ep.auth ? await deps.tokens().accessToken() : undefined
    let res = await request(token)
    if (res.status === 401 && token) {
      token = await deps.tokens().refresh(token)
      res = await request(token)
      // 刚换的令牌也被拒绝：会话已在服务端失效
      if (res.status === 401) deps.tokens().end("expired")
    }

    const text = await res.text()
    if (!res.ok) throw parseErrorResponse(res.status, text)
    return fromJson(ep.output, text ? JSON.parse(text) : {}, { ignoreUnknownFields: true })
  }
  return { send }
}
