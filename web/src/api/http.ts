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

// 按 google.api.http 的规则拼出请求地址：路径里的 {field} 取请求中的同名字段；
// GET 的其余字段放进查询串（Kratos 同时接受 proto 字段名和 JSON 名，这里用 toJson 输出的 JSON 名，默认值不出现）
export function buildRequest(
  method: HttpMethod,
  path: string,
  json: Record<string, unknown>,
  jsonName: (field: string) => string,
): { url: string; body?: string } {
  const rest = { ...json }
  const url = path.replace(/\{([a-z_]+)\}/g, (_, field: string) => {
    const key = jsonName(field)
    const value = rest[key]
    delete rest[key]
    if (value === undefined || value === null || value === "") throw new Error(`missing path parameter ${field}`)
    return encodeURIComponent(String(value))
  })
  if (method !== "GET") return { url, body: JSON.stringify(json) }
  const query = new URLSearchParams()
  for (const [key, value] of Object.entries(rest)) {
    // 目前的查询参数都是标量；数组和对象没有用到，出现时说明接口定义变了
    if (typeof value === "object") throw new Error(`unsupported query parameter ${key}`)
    query.set(key, String(value))
  }
  const qs = query.toString()
  return { url: qs ? `${url}?${qs}` : url }
}

export function createHttp(deps: HttpDeps): { send: Send } {
  const send: Send = async (ep, req) => {
    const json = toJson(ep.input, create(ep.input, req)) as Record<string, unknown>
    const jsonName = (field: string) => ep.input.fields.find((f) => f.name === field)?.jsonName ?? field
    const { url, body } = buildRequest(ep.method, ep.path, json, jsonName)

    const request = async (token: string | undefined) => {
      const headers: Record<string, string> = { Accept: "application/json" }
      if (body !== undefined) headers["Content-Type"] = "application/json"
      if (token) headers.Authorization = `Bearer ${token}`
      try {
        return await deps.fetch(url, { method: ep.method, headers, body })
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
