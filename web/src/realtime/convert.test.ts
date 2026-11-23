import { describe, expect, it } from "vitest"
import { create } from "@bufbuild/protobuf"
import { ConversationSchema, ConversationType } from "@/gen/message/v1/message_pb"
import { GroupStatus } from "@/gen/social/v1/social_pb"
import { GROUP_DISMISSED_TEXT, toConversation } from "./convert"

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
