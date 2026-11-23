import { describe, expect, it } from "vitest"
import { ApiError, errorText, formatWait, GATEWAY_RATE_LIMITED, NETWORK, parseErrorResponse, UNKNOWN } from "./errors"

describe("parseErrorResponse", () => {
  it("解析 Kratos 错误，metadata 只保留字符串值", () => {
    const body = JSON.stringify({
      code: 429,
      reason: "LOGIN_LOCKED",
      message: "密码错误次数过多",
      metadata: { retry_after_seconds: "125", nested: { a: 1 }, n: 3 },
    })
    const e = parseErrorResponse(429, body)
    expect(e).toBeInstanceOf(ApiError)
    expect(e.status).toBe(429)
    expect(e.reason).toBe("LOGIN_LOCKED")
    expect(e.message).toBe("密码错误次数过多")
    expect(e.metadata).toEqual({ retry_after_seconds: "125" })
    expect(e.retryAfterSeconds).toBe(125)
  })

  it("reason 为空时归为 UNKNOWN", () => {
    const e = parseErrorResponse(500, JSON.stringify({ code: 500, reason: "", message: "boom" }))
    expect(e.reason).toBe(UNKNOWN)
    expect(e.message).toBe("boom")
  })

  it("网关限流的 {error_msg}", () => {
    const e = parseErrorResponse(429, JSON.stringify({ error_msg: "Requests are too frequent, please try again later." }))
    expect(e.reason).toBe(GATEWAY_RATE_LIMITED)
    expect(e.status).toBe(429)
    expect(e.retryAfterSeconds).toBeUndefined()
  })

  it("网关限流的响应体不是 JSON 时也归为网关限流", () => {
    expect(parseErrorResponse(429, "<html>429</html>").reason).toBe(GATEWAY_RATE_LIMITED)
  })

  it("非 JSON 的响应体按状态码归类，message 截断到 200 个字符", () => {
    const e = parseErrorResponse(502, "<html>" + "x".repeat(500))
    expect(e.reason).toBe(UNKNOWN)
    expect(e.status).toBe(502)
    expect(e.message).toHaveLength(200)
  })

  it("空响应体", () => {
    const e = parseErrorResponse(503, "")
    expect(e.reason).toBe(UNKNOWN)
    expect(e.message).toBe("")
  })
})

describe("retryAfterSeconds", () => {
  it.each([["abc"], ["0"], ["-3"], ["1.5"], [""]])("%j 视为没有", (v) => {
    expect(new ApiError(429, "RATE_LIMITED", "", { retry_after_seconds: v }).retryAfterSeconds).toBeUndefined()
  })
})

describe("formatWait", () => {
  it.each([
    [1, "1 秒"],
    [59, "59 秒"],
    [60, "1 分钟"],
    [61, "2 分钟"],
    [900, "15 分钟"],
  ])("%d 秒写作 %s", (seconds, want) => {
    expect(formatWait(seconds)).toBe(want)
  })
})

describe("errorText", () => {
  it("带等待时间的文案", () => {
    const e = new ApiError(429, "LOGIN_LOCKED", "", { retry_after_seconds: "125" })
    expect(errorText(e)).toBe("密码错误次数过多，请 3 分钟后再试，或改用验证码登录")
    expect(errorText(new ApiError(429, "CODE_SEND_TOO_FREQUENT", "", { retry_after_seconds: "42" }))).toBe(
      "验证码发送太频繁，请 42 秒后再试",
    )
  })

  it("没有等待时间时按 60 秒写", () => {
    expect(errorText(new ApiError(429, "RATE_LIMITED", ""))).toBe("操作太频繁，请 1 分钟后再试")
  })

  it("固定文案，不展示服务端的 message", () => {
    expect(errorText(new ApiError(401, "INVALID_CREDENTIALS", "server text"))).toBe("邮箱或密码错误")
    expect(errorText(new ApiError(429, GATEWAY_RATE_LIMITED, "Requests are too frequent"))).toBe("操作太频繁，请稍后再试")
    expect(errorText(new ApiError(0, NETWORK, "Failed to fetch"))).toBe("网络连接失败，请检查网络后重试")
  })

  it("overrides 按场景替换文案，没有覆盖的原因仍用默认文案", () => {
    const overrides = { NOT_FRIENDS: "只能邀请好友入群" }
    expect(errorText(new ApiError(403, "NOT_FRIENDS", ""), overrides)).toBe("只能邀请好友入群")
    expect(errorText(new ApiError(403, "NOT_FRIENDS", ""))).toBe("你们还不是好友，不能发送消息")
    expect(errorText(new ApiError(409, "GROUP_FULL", ""), overrides)).toBe("群成员已达上限")
  })

  it("没有登记的原因按状态码给出通用文案", () => {
    expect(errorText(new ApiError(502, UNKNOWN, "<html>"))).toBe("服务暂时不可用，请稍后再试")
    expect(errorText(new ApiError(400, "SOMETHING_NEW", "x"))).toBe("操作失败，请稍后再试")
  })

  it("不是 ApiError", () => {
    expect(errorText(new Error("x"))).toBe("操作失败，请稍后再试")
    expect(errorText("x")).toBe("操作失败，请稍后再试")
  })
})
