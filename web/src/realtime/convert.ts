// 接口与推送中的 protobuf 消息转换为界面使用的数据结构（features/chat/types.ts）。

import { timestampMs } from "@bufbuild/protobuf/wkt"
import { Gender, type UserProfile } from "@/gen/account/v1/account_pb"
import {
  ConversationType,
  type Conversation as PbConversation,
  type Message as PbMessage,
  type MessageContent as PbContent,
} from "@/gen/message/v1/message_pb"
import { GroupStatus } from "@/gen/social/v1/social_pb"
import type { ChatMessage, ChatUser, Conversation, MessageContent } from "@/features/chat/types"

export const GROUP_DISMISSED_TEXT = "群已解散，不能发送消息"

// 同一发送者的 client_msg_id 唯一。用它做界面上的 ID，发送中的消息收到确认后 ID 不变，不会重新播放入场动画
export const messageId = (senderId: string, clientMsgId: string) => `${senderId}:${clientMsgId}`

export function toContent(c: PbContent | undefined): MessageContent {
  switch (c?.body.case) {
    case "text":
      return { kind: "text", text: c.body.value.text }
    case "image": {
      const v = c.body.value
      return {
        kind: "image",
        image: {
          fileId: v.fileId.toString(),
          accessKey: v.accessKey,
          name: v.name,
          size: Number(v.size),
          mimeType: v.mimeType,
          width: v.width,
          height: v.height,
        },
      }
    }
    case "file": {
      const v = c.body.value
      return {
        kind: "file",
        file: { fileId: v.fileId.toString(), accessKey: v.accessKey, name: v.name, size: Number(v.size), mimeType: v.mimeType },
      }
    }
    default:
      // 新版本服务端增加的消息类型，旧客户端显示占位文字
      return { kind: "text", text: "[暂不支持的消息类型]" }
  }
}

export function toChatMessage(m: PbMessage): ChatMessage {
  const senderId = m.senderId.toString()
  const seq = Number(m.seq)
  return {
    id: messageId(senderId, m.clientMsgId || `seq-${seq}`),
    conversationId: m.conversationId.toString(),
    senderId,
    seq,
    content: toContent(m.content),
    sentAt: m.createdAt ? timestampMs(m.createdAt) : Date.now(),
    status: "sent",
  }
}

export function toChatUser(p: UserProfile): ChatUser {
  const id = p.id.toString()
  const user: ChatUser = { id, nickname: p.nickname || `用户 ${id}` }
  if (p.gender === Gender.MALE) user.gender = "male"
  if (p.gender === Gender.FEMALE) user.gender = "female"
  if (p.signature) user.signature = p.signature
  if (p.avatar) user.avatarId = p.avatar
  return user
}

// prev 是本地已有的会话：发送失败得到的「不能发言」原因只存在本地，刷新列表时保留
export function toConversation(c: PbConversation, prev?: Conversation): Conversation {
  const id = c.id.toString()
  const maxSeq = Number(c.maxSeq)
  const readSeq = Number(c.readSeq)
  const base = {
    id,
    maxSeq,
    readSeq,
    peerReadSeq: Number(c.peerReadSeq),
    unread: Math.max(0, maxSeq - readSeq),
    last: c.lastMessage ? toChatMessage(c.lastMessage) : prev?.last,
    blockedReason: prev?.blockedReason,
  }
  if (c.type === ConversationType.GROUP) {
    const dismissed = c.group?.status === GroupStatus.DISMISSED
    return {
      ...base,
      kind: "group",
      // social 不可用时群资料只有 ID
      title: c.group?.name || `群聊 ${id}`,
      avatarId: c.group?.avatar || undefined,
      memberCount: c.group?.memberCount,
      dismissed: dismissed || undefined,
      blockedReason: dismissed ? GROUP_DISMISSED_TEXT : base.blockedReason,
    }
  }
  const peerId = c.peerUser?.id.toString()
  return {
    ...base,
    kind: "single",
    peerId,
    // account 不可用时对方资料只有 ID
    title: c.peerUser?.nickname || `用户 ${peerId ?? id}`,
    avatarId: c.peerUser?.avatar || undefined,
  }
}
