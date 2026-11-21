import { describe, expect, it } from "vitest"
import { isValidPassword, PASSWORD_RULES } from "./password"

const failed = (p: string) => PASSWORD_RULES.filter((r) => !r.test(p)).map((r) => r.id)

describe("密码规则", () => {
  it.each([
    ["abc12345", []],
    ["a1!@#$%^", []],
    ["abc1234", ["length"]],
    ["a1" + "x".repeat(63), ["length"]],
    ["12345678", ["letter"]],
    ["abcdefgh", ["digit"]],
    ["abc 12345", ["charset"]],
    ["abc12345火星", ["charset"]],
    ["", ["length", "letter", "digit"]],
  ])("%j 不满足 %j", (password, want) => {
    expect(failed(password)).toEqual(want)
  })

  it("64 位恰好合法", () => {
    expect(isValidPassword("a1" + "x".repeat(62))).toBe(true)
  })
})
