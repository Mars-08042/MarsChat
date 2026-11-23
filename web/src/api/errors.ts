// HTTP 错误统一解析为 ApiError（设计 3.7）：Kratos 错误 {code, reason, message, metadata}、
// 网关限流的 {error_msg}、网络错误。错误原因到提示文案的映射只在这里维护（设计 3.6.6）。

import { formatBytes } from "@/lib/utils"

// 不是后端 reason 的几种情况
export const NETWORK = "NETWORK"
export const GATEWAY_RATE_LIMITED = "GATEWAY_RATE_LIMITED"
export const UNKNOWN = "UNKNOWN"

export class ApiError extends Error {
  // HTTP 状态码；请求没有发出去或没有收到响应时为 0
  readonly status: number
  readonly reason: string
  readonly metadata: Readonly<Record<string, string>>

  constructor(status: number, reason: string, message: string, metadata: Record<string, string> = {}) {
    super(message)
    this.name = "ApiError"
    this.status = status
    this.reason = reason
    this.metadata = metadata
  }

  // 429 错误附带的等待秒数（account 的 metadata.retry_after_seconds）
  get retryAfterSeconds(): number | undefined {
    const n = Number(this.metadata.retry_after_seconds)
    return Number.isInteger(n) && n > 0 ? n : undefined
  }
}

export function isApiError(e: unknown): e is ApiError {
  return e instanceof ApiError
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v)
}

function stringRecord(v: unknown): Record<string, string> {
  if (!isRecord(v)) return {}
  return Object.fromEntries(Object.entries(v).filter((e): e is [string, string] => typeof e[1] === "string"))
}

// 按响应体解析错误。响应体不是 JSON（例如网关返回的 502 页面）时按状态码归类
export function parseErrorResponse(status: number, body: string): ApiError {
  let json: unknown
  try {
    json = JSON.parse(body)
  } catch {
    json = undefined
  }
  if (isRecord(json) && typeof json.reason === "string") {
    const message = typeof json.message === "string" ? json.message : ""
    return new ApiError(status, json.reason || UNKNOWN, message, stringRecord(json.metadata))
  }
  if (status === 429) {
    const message = isRecord(json) && typeof json.error_msg === "string" ? json.error_msg : "too many requests"
    return new ApiError(status, GATEWAY_RATE_LIMITED, message)
  }
  return new ApiError(status, UNKNOWN, body.slice(0, 200))
}

// 等待时间的口语写法：不足一分钟按秒，否则向上取整到分钟
export function formatWait(seconds: number): string {
  return seconds < 60 ? `${seconds} 秒` : `${Math.ceil(seconds / 60)} 分钟`
}

const wait = (e: ApiError) => formatWait(e.retryAfterSeconds ?? 60)

const TEXT: Record<string, string | ((e: ApiError) => string)> = {
  // account
  CODE_SEND_TOO_FREQUENT: (e) => `验证码发送太频繁，请 ${wait(e)}后再试`,
  CODE_DAILY_LIMIT_EXCEEDED: "这个邮箱今天的验证码次数已用完，请明天再试",
  RATE_LIMITED: (e) => `操作太频繁，请 ${wait(e)}后再试`,
  LOGIN_LOCKED: (e) => `密码错误次数过多，请 ${wait(e)}后再试，或改用验证码登录`,
  CODE_SEND_FAILED: "验证码邮件发送失败，请重试",
  CODE_INVALID: "验证码错误或已过期",
  CODE_ATTEMPTS_EXCEEDED: "验证码错误次数过多，请重新获取",
  WEAK_PASSWORD: "密码必须同时包含字母和数字",
  INVALID_CREDENTIALS: "邮箱或密码错误",
  REFRESH_TOKEN_INVALID: "登录已过期，请重新登录",
  USER_BANNED: "账号已被封禁",
  PERMISSION_DENIED: "没有权限执行这个操作",
  USER_NOT_FOUND: "用户不存在",
  // 踢掉已经下线的设备（不存在和属于别人不做区分）
  SESSION_NOT_FOUND: "这台设备已经下线",
  EMAIL_ALREADY_REGISTERED: "这个邮箱已经注册过了，可以直接登录",
  DEPENDENCY_UNAVAILABLE: "服务暂时不可用，请稍后再试",
  // 发消息（message 与 social 的拒绝原因，经 WebSocket 的 Error 帧返回）
  NOT_FRIENDS: "你们还不是好友，不能发送消息",
  BLOCKED: "你们之间存在拉黑关系，不能发送消息",
  NOT_GROUP_MEMBER: "你已不在这个群里，不能发送消息",
  MEMBER_MUTED: "你已被禁言",
  GROUP_MUTED: "群已开启全员禁言，只有群主和管理员可以发言",
  GROUP_DISMISSED: "群已解散，不能发送消息",
  FILE_INVALID: "文件已失效，请重新选择",
  // 上传（media）。限流、依赖不可用、网络中断在上传场景下的文案见 UPLOAD_OVERRIDES
  FILE_TOO_LARGE: (e) => {
    const max = Number(e.metadata.max_bytes)
    return Number.isFinite(max) && max > 0 ? `文件超过 ${formatBytes(max)} 上限` : "文件太大，超过了上传上限"
  },
  UNSUPPORTED_FILE_TYPE: "头像只支持 JPEG、PNG、GIF、WebP 图片",
  UPLOAD_INCOMPLETE: "网络中断，文件没有传完",
  CONVERSATION_NOT_FOUND: "会话不存在或你已不是成员",
  UPSTREAM_UNAVAILABLE: "服务暂时不可用，请稍后再试",
  CIRCUITBREAKER: "服务暂时不可用，请稍后再试",
  // 好友与群（social 的 HTTP 接口）。上面发消息的几条文案带「不能发送消息」，在好友、群页面调用时用 overrides 换掉
  CANNOT_TARGET_SELF: "不能对自己执行这个操作",
  FRIEND_REQUEST_NOT_ALLOWED: "你们之间存在拉黑关系，不能申请好友",
  GROUP_PERMISSION_DENIED: "你在群里的身份不能执行这个操作",
  FRIEND_REQUEST_NOT_FOUND: "好友申请不存在",
  GROUP_NOT_FOUND: "群不存在",
  GROUP_JOIN_REQUEST_NOT_FOUND: "入群申请不存在",
  GROUP_MEMBER_NOT_FOUND: "对方已不在群里",
  ALREADY_FRIENDS: "你们已经是好友了",
  REQUEST_ALREADY_HANDLED: "申请已被处理",
  ALREADY_GROUP_MEMBER: "已经是群成员了",
  GROUP_FULL: "群成员已达上限",
  GROUP_VERSION_CONFLICT: "群资料刚被其他人修改过，已显示最新内容，请确认后重新保存",
  OWNER_CANNOT_LEAVE: "群主需要先转让群主才能退群",
  // 各服务通用
  UNAUTHORIZED: "登录已失效，请重新登录",
  VALIDATOR: "填写的内容格式不正确",
  // 客户端归类
  [GATEWAY_RATE_LIMITED]: "操作太频繁，请稍后再试",
  [NETWORK]: "网络连接失败，请检查网络后重试",
}

export type ErrorOverrides = Readonly<Record<string, string | ((e: ApiError) => string)>>

// 给用户看的错误提示。没有登记的原因按状态码给出通用文案，不直接展示服务端的 message。
// 同一个 reason 在不同场景含义不同（例如 NOT_FRIENDS 在发消息和邀请入群时），由 overrides 按场景替换
export function errorText(e: unknown, overrides?: ErrorOverrides): string {
  if (!isApiError(e)) return "操作失败，请稍后再试"
  const text = overrides?.[e.reason] ?? TEXT[e.reason]
  if (typeof text === "function") return text(e)
  if (text) return text
  return e.status >= 500 ? "服务暂时不可用，请稍后再试" : "操作失败，请稍后再试"
}

// 上传文件和头像时的错误提示（设计 3.6.9 的错误表）
const UPLOAD_OVERRIDES: ErrorOverrides = {
  RATE_LIMITED: (e) => `上传太频繁，请 ${wait(e)}后再试`,
  DEPENDENCY_UNAVAILABLE: "文件服务暂时不可用，请稍后重试",
  [NETWORK]: "网络中断，文件没有传完",
}

export const uploadErrorText = (e: unknown) => errorText(e, UPLOAD_OVERRIDES)

// 只按 reason 取文案（没有 ApiError 的场合，例如根据群资料推算出的「不能发言」原因）
export function reasonText(reason: string): string | undefined {
  const text = TEXT[reason]
  return typeof text === "string" ? text : undefined
}
