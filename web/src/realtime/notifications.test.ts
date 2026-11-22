import { describe, expect, it } from "vitest"
import { create, type MessageInitShape } from "@bufbuild/protobuf"
import { timestampFromMs } from "@bufbuild/protobuf/wkt"
import { NotificationSchema } from "@/gen/access/v1/frame_pb"
import { LeaveReason } from "@/gen/social/v1/events_pb"
import { GroupRole } from "@/gen/social/v1/social_pb"
import type { Role } from "@/features/groups/model"
import { queryKeys } from "@/lib/query"
import { createDeduper, namesNeeded, notificationEffects, type NotificationContext } from "./notifications"

const ME = 1n
const LIN = 2n
const GROUP = 100n
const NOW = new Date(2026, 10, 22, 10, 0).getTime()

type Body = MessageInitShape<typeof NotificationSchema>["body"]

function ctx(prev?: { role: Role; mutedUntil?: number }): NotificationContext {
  return {
    meId: ME.toString(),
    userName: (id) => ({ "2": "林晚" })[id],
    groupName: (id) => (id === GROUP.toString() ? "周末登山小队" : undefined),
    previousSelf: () => prev,
    now: NOW,
  }
}

const effects = (body: Body, c = ctx()) => notificationEffects(create(NotificationSchema, { eventId: "e1", body }), c)

describe("notificationEffects 好友", () => {
  it("收到好友申请：刷新收到的申请，提示带附言和查看按钮", () => {
    const e = effects({ case: "friendRequestCreated", value: { requestId: 9n, fromUserId: LIN, toUserId: ME, message: "我是林晚" } })
    expect(e.invalidate).toEqual([queryKeys.receivedRequests])
    expect(e.toast).toEqual({ title: "林晚 请求添加你为好友", description: "我是林晚", action: { label: "查看", to: "/contacts/requests" } })
  })

  it("不认识申请人时用通用标题", () => {
    const e = effects({ case: "friendRequestCreated", value: { requestId: 9n, fromUserId: 77n, toUserId: ME } })
    expect(e.toast?.title).toBe("收到新的好友申请")
    expect(e.toast?.description).toBeUndefined()
  })

  it("对方同意了我的申请：提示、庆祝，并清除单聊的「不是好友」", () => {
    const e = effects({ case: "friendAdded", value: { userId: LIN, friendId: ME } })
    expect(e.invalidate).toEqual([queryKeys.friends, queryKeys.receivedRequests, queryKeys.sentRequests])
    expect(e.friendship).toEqual({ userId: "2", friends: true })
    expect(e.toast?.title).toBe("你和 林晚 已成为好友")
    expect(e.toast?.action?.to).toBe("/chat/with/2")
    expect(e.celebrate).toBe(true)
  })

  it("自己同意的申请只刷新，提示和庆祝由界面负责", () => {
    const e = effects({ case: "friendAdded", value: { userId: ME, friendId: LIN } })
    expect(e.friendship).toEqual({ userId: "2", friends: true })
    expect(e.toast).toBeUndefined()
    expect(e.celebrate).toBeUndefined()
  })

  it("删除好友不提示，单聊出现「不是好友」", () => {
    const e = effects({ case: "friendDeleted", value: { userId: LIN, friendId: ME } })
    expect(e).toEqual({ invalidate: [queryKeys.friends], friendship: { userId: "2", friends: false } })
  })
})

describe("notificationEffects 群", () => {
  it("被拉进新群：刷新我的群并取会话，提示可以跳转", () => {
    const e = effects({ case: "groupCreated", value: { group: { id: GROUP, name: "新群", ownerId: LIN }, memberIds: [LIN, ME] } })
    expect(e.invalidate).toEqual([queryKeys.groups])
    expect(e.refreshConversation).toBe("100")
    expect(e.toast).toEqual({ title: "你已加入群「新群」", action: { label: "查看", to: "/chat/100" } })
    expect(e.selfMember).toEqual({ groupId: "100", role: "member" })
  })

  it("自己建的群不提示，记下自己是群主", () => {
    const e = effects({ case: "groupCreated", value: { group: { id: GROUP, name: "新群", ownerId: ME }, memberIds: [ME] } })
    expect(e.toast).toBeUndefined()
    expect(e.refreshConversation).toBe("100")
    expect(e.selfMember).toEqual({ groupId: "100", role: "owner" })
  })

  it("别人入群：刷新群资料与成员、会话的成员数，不提示", () => {
    const e = effects({ case: "groupMembersJoined", value: { groupId: GROUP, userIds: [3n], operatorId: LIN } })
    expect(e.invalidate).toEqual([queryKeys.groups, ["group", "100"]])
    expect(e.refreshConversation).toBe("100")
    expect(e.recheckGroup).toBeUndefined()
    expect(e.toast).toBeUndefined()
  })

  it("自己被邀请或申请被同意：提示并重新计算发言权限", () => {
    const e = effects({ case: "groupMembersJoined", value: { groupId: GROUP, userIds: [ME, 3n], operatorId: LIN } })
    expect(e.recheckGroup).toBe("100")
    expect(e.toast?.title).toBe("你已加入群「周末登山小队」")
    expect(e.selfMember).toEqual({ groupId: "100", role: "member" })
  })

  it("自己直接加入的群不提示", () => {
    const e = effects({ case: "groupMembersJoined", value: { groupId: GROUP, userIds: [ME], operatorId: ME } })
    expect(e.toast).toBeUndefined()
    expect(e.recheckGroup).toBe("100")
  })

  it("被移出群：移除会话并提示；自己退群不提示", () => {
    const removed = effects({ case: "groupMemberLeft", value: { groupId: GROUP, userId: ME, reason: LeaveReason.REMOVED, operatorId: LIN } })
    expect(removed.removeConversation).toBe("100")
    expect(removed.toast?.title).toBe("你已被移出群「周末登山小队」")
    const left = effects({ case: "groupMemberLeft", value: { groupId: GROUP, userId: ME, reason: LeaveReason.LEFT, operatorId: ME } })
    expect(left.removeConversation).toBe("100")
    expect(left.toast).toBeUndefined()
  })

  it("别人退群只刷新", () => {
    const e = effects({ case: "groupMemberLeft", value: { groupId: GROUP, userId: 3n, reason: LeaveReason.LEFT, operatorId: 3n } })
    expect(e).toEqual({ invalidate: [queryKeys.groups, ["group", "100"]], refreshConversation: "100" })
  })

  it("自己被禁言：提示截止时间并重新计算发言权限", () => {
    const until = new Date(2026, 10, 22, 10, 30).getTime()
    const e = effects(
      { case: "groupMemberUpdated", value: { groupId: GROUP, userId: ME, role: GroupRole.MEMBER, mutedUntil: timestampFromMs(until), operatorId: LIN } },
      ctx({ role: "member" }),
    )
    expect(e.recheckGroup).toBe("100")
    expect(e.toast?.title).toBe("你在群「周末登山小队」被禁言，今天 10:30 解除")
    expect(e.selfMember).toEqual({ groupId: "100", role: "member", mutedUntil: until })
  })

  it("禁言解除、被设为管理员、取消管理员、成为群主都有提示", () => {
    const update = (role: GroupRole, prev: { role: Role; mutedUntil?: number }) =>
      effects({ case: "groupMemberUpdated", value: { groupId: GROUP, userId: ME, role, operatorId: LIN } }, ctx(prev)).toast?.title
    expect(update(GroupRole.MEMBER, { role: "member", mutedUntil: NOW + 60_000 })).toBe("你在群「周末登山小队」的禁言已解除")
    expect(update(GroupRole.ADMIN, { role: "member" })).toBe("你已成为群「周末登山小队」的管理员")
    expect(update(GroupRole.MEMBER, { role: "admin" })).toBe("你在群「周末登山小队」的管理员身份已被取消")
    expect(update(GroupRole.OWNER, { role: "admin" })).toBe("你已成为群「周末登山小队」的群主")
  })

  it("不知道修改前的状态时，只在被禁言时提示", () => {
    const e = effects({ case: "groupMemberUpdated", value: { groupId: GROUP, userId: ME, role: GroupRole.ADMIN, operatorId: LIN } })
    expect(e.toast).toBeUndefined()
    expect(e.recheckGroup).toBe("100")
    // 仍然记下新状态，下一条成员变更通知就能对比出变化
    expect(e.selfMember).toEqual({ groupId: "100", role: "admin", mutedUntil: undefined })
  })

  it("自己转让群主时不提示，但记下自己的新角色", () => {
    const e = effects({ case: "groupMemberUpdated", value: { groupId: GROUP, userId: ME, role: GroupRole.MEMBER, operatorId: ME } }, ctx({ role: "owner" }))
    expect(e.toast).toBeUndefined()
    expect(e.selfMember).toEqual({ groupId: "100", role: "member", mutedUntil: undefined })
  })

  it("别人的角色变化只刷新群数据", () => {
    const e = effects({ case: "groupMemberUpdated", value: { groupId: GROUP, userId: 3n, role: GroupRole.ADMIN, operatorId: ME } })
    expect(e).toEqual({ invalidate: [["group", "100"]] })
  })

  it("群资料变化：刷新会话标题并重新计算发言权限（全员禁言）", () => {
    const e = effects({ case: "groupUpdated", value: { group: { id: GROUP, name: "改名了", muteAll: true }, operatorId: LIN } })
    expect(e.refreshConversation).toBe("100")
    expect(e.recheckGroup).toBe("100")
    expect(e.toast).toBeUndefined()
  })

  it("群被解散：别人解散时提示，会话保留并刷新", () => {
    const e = effects({ case: "groupDismissed", value: { groupId: GROUP, memberIds: [ME, LIN], operatorId: LIN } })
    expect(e.refreshConversation).toBe("100")
    expect(e.removeConversation).toBeUndefined()
    expect(e.toast?.title).toBe("群「周末登山小队」已解散")
    const mine = effects({ case: "groupDismissed", value: { groupId: GROUP, memberIds: [ME], operatorId: ME } })
    expect(mine.toast).toBeUndefined()
  })

  it("入群申请：刷新申请列表，提示可以打开群面板处理", () => {
    const e = effects({ case: "groupJoinRequestCreated", value: { requestId: 5n, groupId: GROUP, userId: LIN, message: "带我一个" } })
    expect(e.invalidate).toEqual([queryKeys.joinRequests("100")])
    expect(e.toast).toEqual({
      title: "林晚 申请加入群「周末登山小队」",
      description: "带我一个",
      action: { label: "处理", to: "/chat/100", panel: "requests" },
    })
  })

  it("不认识的通知类型什么也不做", () => {
    expect(effects({ case: undefined })).toEqual({ invalidate: [] })
  })
})

describe("namesNeeded", () => {
  const needed = (body: Body) => namesNeeded(create(NotificationSchema, { eventId: "e1", body }), ME.toString())
  const none = { users: [], groups: [] }

  it("好友申请和成为好友的提示需要对方的名字；自己发起的不需要", () => {
    expect(needed({ case: "friendRequestCreated", value: { requestId: 9n, fromUserId: LIN, toUserId: ME } })).toEqual({ users: ["2"], groups: [] })
    expect(needed({ case: "friendRequestCreated", value: { requestId: 9n, fromUserId: ME, toUserId: LIN } })).toEqual(none)
    expect(needed({ case: "friendAdded", value: { userId: LIN, friendId: ME } })).toEqual({ users: ["2"], groups: [] })
    expect(needed({ case: "friendAdded", value: { userId: ME, friendId: LIN } })).toEqual(none)
  })

  it("入群申请需要申请人和群名", () => {
    expect(needed({ case: "groupJoinRequestCreated", value: { requestId: 9n, groupId: GROUP, userId: 3n } })).toEqual({ users: ["3"], groups: ["100"] })
  })

  it("只有会弹提示的群通知才需要群名", () => {
    const g = { users: [], groups: ["100"] }
    expect(needed({ case: "groupMembersJoined", value: { groupId: GROUP, userIds: [ME], operatorId: LIN } })).toEqual(g)
    expect(needed({ case: "groupMembersJoined", value: { groupId: GROUP, userIds: [ME], operatorId: ME } })).toEqual(none)
    expect(needed({ case: "groupMembersJoined", value: { groupId: GROUP, userIds: [3n], operatorId: LIN } })).toEqual(none)
    expect(needed({ case: "groupMemberLeft", value: { groupId: GROUP, userId: ME, reason: LeaveReason.REMOVED, operatorId: LIN } })).toEqual(g)
    expect(needed({ case: "groupMemberLeft", value: { groupId: GROUP, userId: ME, reason: LeaveReason.LEFT, operatorId: ME } })).toEqual(none)
    expect(needed({ case: "groupMemberUpdated", value: { groupId: GROUP, userId: ME, role: GroupRole.ADMIN, operatorId: LIN } })).toEqual(g)
    expect(needed({ case: "groupMemberUpdated", value: { groupId: GROUP, userId: 3n, role: GroupRole.ADMIN, operatorId: LIN } })).toEqual(none)
    expect(needed({ case: "groupDismissed", value: { groupId: GROUP, operatorId: LIN } })).toEqual(g)
    expect(needed({ case: "groupDismissed", value: { groupId: GROUP, operatorId: ME } })).toEqual(none)
  })
})

describe("createDeduper", () => {
  it("同一个 event_id 只处理一次", () => {
    const fresh = createDeduper()
    expect(fresh("a")).toBe(true)
    expect(fresh("a")).toBe(false)
    expect(fresh("b")).toBe(true)
  })

  it("超过容量时忘掉最早的记录", () => {
    const fresh = createDeduper(2)
    fresh("a")
    fresh("b")
    fresh("c")
    expect(fresh("a")).toBe(true)
    expect(fresh("c")).toBe(false)
  })

  it("没有 event_id 的通知不去重", () => {
    const fresh = createDeduper()
    expect(fresh("")).toBe(true)
    expect(fresh("")).toBe(true)
  })
})
