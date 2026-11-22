// HTTP 错误统一解析为 ApiError（设计 3.7）：Kratos 错误 {code, reason, message, metadata}、
// 网关限流的 {error_msg}、网络错误。错误原因到提示文案的映射只在这里维护（设计 3.6.6）。

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
  EMAIL_ALREADY_REGISTERED: "这个邮箱已经注册过了，可以直接登录",
  DEPENDENCY_UNAVAILABLE: "服务暂时不可用，请稍后再试",
  // 各服务通用
  UNAUTHORIZED: "登录已失效，请重新登录",
  VALIDATOR: "填写的内容格式不正确",
  // 客户端归类
  [GATEWAY_RATE_LIMITED]: "操作太频繁，请稍后再试",
  [NETWORK]: "网络连接失败，请检查网络后重试",
}

// 给用户看的错误提示。没有登记的原因按状态码给出通用文案，不直接展示服务端的 message
export function errorText(e: unknown): string {
  if (!isApiError(e)) return "操作失败，请稍后再试"
  const text = TEXT[e.reason]
  if (typeof text === "function") return text(e)
  if (text) return text
  return e.status >= 500 ? "服务暂时不可用，请稍后再试" : "操作失败，请稍后再试"
}
