import { describe, expect, it } from "vitest"
import { canManage, groupBlock, isManager, sortMembers, type MemberItem, type Role } from "./model"

const NOW = 1_000_000
const normal = { dismissed: false, myRole: "member" as const, muteAll: false }

describe("groupBlock", () => {
  it("正常成员可以发言", () => {
    expect(groupBlock(normal, undefined, NOW)).toBeUndefined()
  })

  it("按 social 的检查顺序：解散、不是成员、单人禁言、全员禁言", () => {
    expect(groupBlock({ ...normal, dismissed: true, myRole: "none" }, NOW + 1, NOW)).toEqual({ reason: "GROUP_DISMISSED" })
    expect(groupBlock({ ...normal, myRole: "none", muteAll: true }, undefined, NOW)).toEqual({ reason: "NOT_GROUP_MEMBER" })
    expect(groupBlock({ ...normal, muteAll: true }, NOW + 60_000, NOW)).toEqual({ reason: "MEMBER_MUTED", until: NOW + 60_000 })
    expect(groupBlock({ ...normal, muteAll: true }, undefined, NOW)).toEqual({ reason: "GROUP_MUTED" })
  })

  it("禁言已过期不算", () => {
    expect(groupBlock(normal, NOW - 1, NOW)).toBeUndefined()
    expect(groupBlock(normal, NOW, NOW)).toBeUndefined()
  })

  it("全员禁言不限制群主和管理员", () => {
    expect(groupBlock({ ...normal, myRole: "admin", muteAll: true }, undefined, NOW)).toBeUndefined()
    expect(groupBlock({ ...normal, myRole: "owner", muteAll: true }, undefined, NOW)).toBeUndefined()
  })
})

describe("成员管理权限", () => {
  it("群主管理员和成员，管理员只管成员，没人能管群主", () => {
    expect(canManage("owner", "admin")).toBe(true)
    expect(canManage("owner", "member")).toBe(true)
    expect(canManage("owner", "owner")).toBe(false)
    expect(canManage("admin", "member")).toBe(true)
    expect(canManage("admin", "admin")).toBe(false)
    expect(canManage("admin", "owner")).toBe(false)
    expect(canManage("member", "member")).toBe(false)
  })

  it("群主和管理员可以改资料、审批、邀请", () => {
    expect(isManager("owner")).toBe(true)
    expect(isManager("admin")).toBe(true)
    expect(isManager("member")).toBe(false)
    expect(isManager("none")).toBe(false)
  })

  it("成员按群主、管理员、成员排序，同级按入群时间", () => {
    const m = (id: string, role: Role, joinedAt: number): MemberItem => ({ user: { id, nickname: id }, role, joinedAt })
    const sorted = sortMembers([m("a", "member", 1), m("b", "admin", 5), m("c", "owner", 9), m("d", "member", 0), m("e", "admin", 2)])
    expect(sorted.map((x) => x.user.id)).toEqual(["c", "e", "b", "d", "a"])
  })
})
