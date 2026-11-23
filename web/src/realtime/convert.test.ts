import { describe, expect, it } from "vitest"
import { create } from "@bufbuild/protobuf"
import { Gender, UserSchema } from "@/gen/account/v1/account_pb"
import { ConversationSchema, ConversationType } from "@/gen/message/v1/message_pb"
import { GroupStatus } from "@/gen/social/v1/social_pb"
import { GROUP_DISMISSED_TEXT, toChatUser, toConversation } from "./convert"

describe("toChatUser", () => {
  it("自己的资料（GetMe 的 User）带上头像、性别和签名，时间线里自己的头像才能显示", () => {
    const me = create(UserSchema, { id: 7n, nickname: "阿星", avatar: "f-1", gender: Gender.MALE, signature: "写代码" })
    expect(toChatUser(me)).toEqual({ id: "7", nickname: "阿星", avatarId: "f-1", gender: "male", signature: "写代码" })
  })

  it("没有设置的字段不出现", () => {
    expect(toChatUser(create(UserSchema, { id: 8n, nickname: "" }))).toEqual({ id: "8", nickname: "用户 8" })
  })
})

const group = (status: GroupStatus, memberCount: number) =>
  create(ConversationSchema, {
    id: 100n,
    type: ConversationType.GROUP,
    maxSeq: 5n,
    readSeq: 3n,
    group: { id: 100n, name: "周末登山小队", status, memberCount },
  })

describe("toConversation 群聊", () => {
  it("正常的群显示成员数，可以发言", () => {
    const c = toConversation(group(GroupStatus.NORMAL, 6))
    expect(c).toMatchObject({ id: "100", kind: "group", title: "周末登山小队", memberCount: 6, unread: 2 })
    expect(c.dismissed).toBeUndefined()
    expect(c.blockedReason).toBeUndefined()
  })

  it("已解散的群标记为已解散，并给出不能发言的原因", () => {
    const c = toConversation(group(GroupStatus.DISMISSED, 0))
    expect(c.dismissed).toBe(true)
    expect(c.blockedReason).toBe(GROUP_DISMISSED_TEXT)
  })

  it("刷新时保留本地得到的不能发言原因", () => {
    const prev = toConversation(group(GroupStatus.NORMAL, 6))
    const c = toConversation(group(GroupStatus.NORMAL, 6), { ...prev, blockedReason: "你已被禁言" })
    expect(c.blockedReason).toBe("你已被禁言")
  })
})
