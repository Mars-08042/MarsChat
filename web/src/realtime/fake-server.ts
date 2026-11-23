// realtime 单元测试用的内存假服务端：按 message、social 接口的规则实现分页、补拉的三种结果和已读位置。
// 不被业务代码引用。

import { create } from "@bufbuild/protobuf"
import { timestampFromMs } from "@bufbuild/protobuf/wkt"
import { UserProfileSchema } from "@/gen/account/v1/account_pb"
import {
  ConversationSchema,
  ConversationType,
  DifferenceResult,
  GetDifferenceResponseSchema,
  ListConversationsResponseSchema,
  ListMessagesResponseSchema,
  MessageSchema,
  type Message,
  type ReadPosition,
} from "@/gen/message/v1/message_pb"
import { GroupStatus, ListGroupMembersResponseSchema } from "@/gen/social/v1/social_pb"
import type { SyncApi } from "./sync"

// 与后端 policy.difference_too_long 一致
export const TOO_LONG = 1000
const T0 = 1_800_000_000_000

interface FakeConversation {
  id: bigint
  type: ConversationType
  peer?: { id: bigint; nickname: string }
  group?: { name: string; members: Array<{ id: bigint; nickname: string }>; dismissed?: boolean }
  messages: Message[]
  read: Map<bigint, bigint>
}

export function fakeServer(me: bigint) {
  const conversations = new Map<bigint, FakeConversation>()
  const calls: string[] = []

  const conv = (id: bigint) => {
    const c = conversations.get(id)
    if (!c) throw new Error(`no conversation ${id}`)
    return c
  }
  const maxSeq = (c: FakeConversation) => BigInt(c.messages.length)

  const toPb = (c: FakeConversation) => {
    const readSeq = c.read.get(me) ?? 0n
    const peerRead = c.peer ? (c.read.get(c.peer.id) ?? 0n) : 0n
    return create(ConversationSchema, {
      id: c.id,
      type: c.type,
      peerUser: c.peer ? { id: c.peer.id, nickname: c.peer.nickname } : undefined,
      group: c.group
        ? {
            id: c.id,
            name: c.group.name,
            memberCount: c.group.members.length,
            status: c.group.dismissed ? GroupStatus.DISMISSED : GroupStatus.NORMAL,
          }
        : undefined,
      maxSeq: maxSeq(c),
      readSeq,
      unreadCount: maxSeq(c) - readSeq,
      peerReadSeq: peerRead,
      lastMessage: c.messages.at(-1),
    })
  }

  const server = {
    calls,
    addSingle(id: bigint, peer: { id: bigint; nickname: string }) {
      conversations.set(id, { id, type: ConversationType.SINGLE, peer, messages: [], read: new Map() })
    },
    addGroup(id: bigint, name: string, members: Array<{ id: bigint; nickname: string }>, dismissed = false) {
      conversations.set(id, { id, type: ConversationType.GROUP, group: { name, members, dismissed }, messages: [], read: new Map() })
    },
    remove(id: bigint) {
      conversations.delete(id)
    },
    // 追加一条消息，发送者的已读位置随之推进（与 message 服务一致）
    post(id: bigint, sender: bigint, text: string, clientMsgId = `c${id}-${conv(id).messages.length + 1}`): Message {
      const c = conv(id)
      const seq = maxSeq(c) + 1n
      const m = create(MessageSchema, {
        conversationId: id,
        conversationType: c.type,
        seq,
        senderId: sender,
        clientMsgId,
        content: { body: { case: "text", value: { text } } },
        createdAt: timestampFromMs(T0 + Number(seq) * 1000),
      })
      c.messages.push(m)
      c.read.set(sender, seq)
      return m
    },
    postMany(id: bigint, sender: bigint, n: number): Message[] {
      return Array.from({ length: n }, (_, i) => server.post(id, sender, `m${i}`))
    },
    readOf(id: bigint, user: bigint) {
      return conv(id).read.get(user) ?? 0n
    },
    readPosition(id: bigint, user: bigint, readSeq: bigint): ReadPosition {
      return { $typeName: "message.v1.ReadPosition", conversationId: id, userId: user, readSeq }
    },
  }

  const api: SyncApi = {
    listConversations: async (cursor, limit) => {
      calls.push("listConversations")
      const all = [...conversations.values()].sort((a, b) => (a.id < b.id ? -1 : 1)).filter((c) => c.id > cursor)
      const page = all.slice(0, limit)
      return create(ListConversationsResponseSchema, {
        conversations: page.map(toPb),
        nextCursor: all.length > limit ? page[page.length - 1].id : 0n,
      })
    },
    getConversation: async (id) => {
      calls.push(`getConversation:${id}`)
      const c = conversations.get(id)
      return c ? toPb(c) : undefined
    },
    listMessages: async (id, beforeSeq, limit) => {
      calls.push(`listMessages:${id}:${beforeSeq}`)
      const c = conv(id)
      const older = c.messages.filter((m) => beforeSeq === 0n || m.seq < beforeSeq).reverse()
      const page = older.slice(0, limit)
      return create(ListMessagesResponseSchema, {
        messages: page,
        nextBeforeSeq: older.length > limit ? page[page.length - 1].seq : 0n,
      })
    },
    getDifference: async (id, fromSeq, limit) => {
      calls.push(`getDifference:${id}:${fromSeq}`)
      const c = conv(id)
      if (maxSeq(c) - fromSeq > BigInt(TOO_LONG)) {
        return create(GetDifferenceResponseSchema, { result: DifferenceResult.TOO_LONG, maxSeq: maxSeq(c) })
      }
      const after = c.messages.filter((m) => m.seq > fromSeq)
      const page = after.slice(0, limit)
      const more = after.length > limit
      return create(GetDifferenceResponseSchema, {
        result: more ? DifferenceResult.SLICE : DifferenceResult.COMPLETE,
        messages: page,
        nextFromSeq: more ? page[page.length - 1].seq : 0n,
        maxSeq: maxSeq(c),
      })
    },
    markRead: async (id, seq) => {
      calls.push(`markRead:${id}:${seq}`)
      const c = conv(id)
      const cur = c.read.get(me) ?? 0n
      const next = seq > maxSeq(c) ? maxSeq(c) : seq
      if (next > cur) c.read.set(me, next)
      return c.read.get(me) ?? 0n
    },
    listGroupMembers: async (groupId) => {
      calls.push(`listGroupMembers:${groupId}`)
      const c = conv(groupId)
      return create(ListGroupMembersResponseSchema, {
        members: (c.group?.members ?? []).map((m) => ({
          userId: m.id,
          profile: create(UserProfileSchema, { id: m.id, nickname: m.nickname }),
        })),
        nextCursor: 0n,
      })
    },
  }

  return { server, api }
}
