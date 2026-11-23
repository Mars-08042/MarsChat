import { describe, expect, it } from "vitest"
import { create } from "@bufbuild/protobuf"
import { Gender, UserSchema } from "@/gen/account/v1/account_pb"
import { CallRecordMedia, CallResult, ConversationSchema, ConversationType, MessageContentSchema } from "@/gen/message/v1/message_pb"
import { GroupStatus } from "@/gen/social/v1/social_pb"
import { contentPreview } from "@/features/chat/types"
import { GROUP_DISMISSED_TEXT, toChatUser, toContent, toConversation, UNSUPPORTED_MESSAGE_TEXT } from "./convert"

describe("toContent 通话记录", () => {
  const call = (media: CallRecordMedia, result: CallResult, durationSeconds = 0) =>
    create(MessageContentSchema, { body: { case: "call", value: { media, result, durationSeconds } } })

  it("转换类型、结果和时长，会话摘要按类型显示", () => {
    const video = toContent(call(CallRecordMedia.VIDEO, CallResult.COMPLETED, 83))
    expect(video).toEqual({ kind: "call", call: { media: "video", result: "completed", durationSeconds: 83 } })
    expect(contentPreview(video)).toBe("[视频通话]")
    const audio = toContent(call(CallRecordMedia.AUDIO, CallResult.MISSED))
    expect(audio).toEqual({ kind: "call", call: { media: "audio", result: "missed", durationSeconds: 0 } })
    expect(contentPreview(audio)).toBe("[语音通话]")
  })

  it("不认识的通话结果按不支持的消息显示", () => {
    expect(toContent(call(CallRecordMedia.AUDIO, 99 as CallResult))).toEqual({ kind: "text", text: UNSUPPORTED_MESSAGE_TEXT })
  })
})

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
