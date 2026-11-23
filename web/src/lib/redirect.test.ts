import { describe, expect, it } from "vitest"
import { HOME_PATH, loginPath, safeRedirect } from "./redirect"

describe("safeRedirect", () => {
  it.each([["/chat/123"], ["/contacts/requests?tab=1#top"], ["/settings/devices"], ["/loginx"]])("站内路径 %s 原样返回", (p) => {
    expect(safeRedirect(p)).toBe(p)
  })

  it.each([
    [null],
    [undefined],
    [""],
    ["chat"],
    ["https://evil.example/"],
    ["javascript:alert(1)"],
    ["//evil.example"],
    ["/\\evil.example"],
    ["/\t/evil.example"],
    ["/\n/evil.example"],
    ["/chat\\..\\x"],
    ["/login"],
    ["/login?redirect=/chat"],
    ["/register#x"],
  ])("%j 回到默认页", (p) => {
    expect(safeRedirect(p)).toBe(HOME_PATH)
  })
})

describe("loginPath", () => {
  it("默认页不带 redirect 参数", () => {
    expect(loginPath({ pathname: "/chat", search: "", hash: "" })).toBe("/login")
  })

  it("带上完整的原路径，登录后能原样取回", () => {
    const path = loginPath({ pathname: "/chat/9", search: "?a=1&b=2", hash: "#m" })
    expect(path).toBe("/login?redirect=%2Fchat%2F9%3Fa%3D1%26b%3D2%23m")
    const redirect = new URLSearchParams(path.split("?")[1]).get("redirect")
    expect(safeRedirect(redirect)).toBe("/chat/9?a=1&b=2#m")
  })
})
