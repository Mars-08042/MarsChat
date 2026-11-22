// 同步引擎（设计 3.9.3；data-flow 5.2、第 7 节）：
// - 认证成功后分页拉取全部会话，只为已经加载过时间线的会话补拉；
// - 推送按 (conversation_id, seq) 去重，seq 不连续时补拉缺口；补拉有 COMPLETE、SLICE、TOO_LONG 三种结果；
// - 未知会话的消息先取会话信息；已读位置只增不减；上报已读防抖 500 毫秒。
// 接口由调用方注入，单元测试用内存中的假服务端。

import { timestampMs } from "@bufbuild/protobuf/wkt"
import type { ReadPosition, SendResult } from "@/gen/message/v1/message_pb"
import {
  DifferenceResult,
  type Conversation as PbConversation,
  type GetDifferenceResponse,
  type ListConversationsResponse,
  type ListMessagesResponse,
  type Message as PbMessage,
} from "@/gen/message/v1/message_pb"
import type { ListGroupMembersResponse } from "@/gen/social/v1/social_pb"
import type { ChatMessage, Conversation } from "@/features/chat/types"
import { draftKey, type ChatStore, type Timeline } from "@/stores/chat"
import { toChatMessage, toChatUser, toConversation } from "./convert"

// ListMessages 每页条数
export const PAGE_SIZE = 30
export const DIFFERENCE_LIMIT = 200
export const CONVERSATION_PAGE = 100
export const MEMBER_PAGE = 100
export const READ_DEBOUNCE_MS = 500

export interface SyncApi {
  listConversations(cursor: bigint, limit: number): Promise<ListConversationsResponse>
  // 会话不存在或不是成员时返回 undefined
  getConversation(id: bigint): Promise<PbConversation | undefined>
  listMessages(id: bigint, beforeSeq: bigint, limit: number): Promise<ListMessagesResponse>
  getDifference(id: bigint, fromSeq: bigint, limit: number): Promise<GetDifferenceResponse>
  markRead(id: bigint, seq: bigint): Promise<bigint>
  listGroupMembers(groupId: bigint, cursor: bigint, limit: number): Promise<ListGroupMembersResponse>
}

export interface SyncLogger {
  warn: (message: string, detail?: Record<string, unknown>) => void
}

export interface SyncDeps {
  store: ChatStore
  api: SyncApi
  // 本轮同步（认证成功后的 syncAll）累计补拉到的消息条数，用于连接状态胶囊
  onProgress?: (fetched: number) => void
  log?: SyncLogger
}

export interface SyncEngine {
  syncAll(): Promise<void>
  handleMessage(m: PbMessage): void
  handleRead(r: ReadPosition): void
  // 发送队列收到 SendAck 后调用；local 是发送中的那条消息
  confirmSent(local: ChatMessage, ack: SendResult): void
  // 打开会话：取会话信息、加载最新一页、计算未读分隔线。结束后会话仍不存在，说明不是成员或已被删除。
  // force 用于加载失败后重试
  open(conversationId: string | undefined, force?: boolean): Promise<void>
  loadOlder(conversationId: string): Promise<void>
  // 界面在会话可见、窗口有焦点、滚动到底部时调用
  reportRead(conversationId: string): void
  // 取会话信息并合并到本地；群资料、成员数、解散状态变化后也用它刷新
  ensureConversation(id: string): Promise<Conversation | undefined>
  // 自己退群或被移出后看不到这个会话了：连同时间线一起删除
  removeConversation(id: string): void
  // 设置或清除「不能发言」的原因（说明条）
  setBlocked(id: string, reason: string | undefined): void
  dispose(): void
}

// 把 incoming 并入已排好序的消息：按 seq 去重（保留已有的对象，避免无谓的重渲染），结果按 seq 升序
export function mergeMessages(existing: readonly ChatMessage[], incoming: readonly ChatMessage[]): ChatMessage[] {
  const bySeq = new Map<number, ChatMessage>()
  for (const m of incoming) if (m.seq !== undefined) bySeq.set(m.seq, m)
  for (const m of existing) if (m.seq !== undefined) bySeq.set(m.seq, m)
  return [...bySeq.values()].sort((a, b) => (a.seq ?? 0) - (b.seq ?? 0))
}

// 从 from 开始，沿着连续的 seq 往后走，返回能走到的最大 seq
export function extendContiguous(from: number, sorted: readonly ChatMessage[]): number {
  let c = from
  for (const m of sorted) {
    const seq = m.seq ?? 0
    if (seq <= c) continue
    if (seq !== c + 1) break
    c = seq
  }
  return c
}

export function mergeTimeline(t: Timeline, incoming: readonly ChatMessage[]): Timeline {
  const messages = mergeMessages(t.messages, incoming)
  return { ...t, messages, contiguousMaxSeq: extendContiguous(t.contiguousMaxSeq, messages) }
}

const maxOf = (a: number, b: number) => (a > b ? a : b)

export function createSyncEngine(deps: SyncDeps): SyncEngine {
  const { store, api } = deps
  const get = store.getState
  const set = store.setState

  const conversationFetches = new Map<string, Promise<Conversation | undefined>>()
  const latestLoads = new Map<string, Promise<void>>()
  const differences = new Map<string, Promise<void>>()
  const differenceAgain = new Set<string>()
  const membersLoaded = new Set<string>()
  const readTimers = new Map<string, ReturnType<typeof setTimeout>>()
  let fetched = 0
  let disposed = false
  let opening: Promise<void> = Promise.resolve()

  const warn = (message: string, e: unknown, detail: Record<string, unknown> = {}) =>
    deps.log?.warn(message, { ...detail, error: String(e) })

  const patchConversation = (id: string, f: (c: Conversation) => Conversation) =>
    set((s) => (s.conversations[id] ? { conversations: { ...s.conversations, [id]: f(s.conversations[id]) } } : {}))

  const setTimeline = (id: string, t: Timeline) => set((s) => ({ timelines: { ...s.timelines, [id]: t } }))

  // 用新到的消息更新会话摘要：最新 seq、最后一条消息、自己发的消息推进自己的已读位置
  const applySummary = (id: string, messages: readonly ChatMessage[]) => {
    if (messages.length === 0) return
    const meId = get().meId
    patchConversation(id, (c) => {
      let { maxSeq, readSeq, last } = c
      for (const m of messages) {
        const seq = m.seq ?? 0
        maxSeq = maxOf(maxSeq, seq)
        if (m.senderId === meId) readSeq = maxOf(readSeq, seq)
        if (!last || seq >= (last.seq ?? 0)) last = m
      }
      return { ...c, maxSeq, readSeq, last, unread: Math.max(0, maxSeq - readSeq) }
    })
  }

  const bumpMaxSeq = (id: string, maxSeq: number) =>
    patchConversation(id, (c) => (maxSeq > c.maxSeq ? { ...c, maxSeq, unread: Math.max(0, maxSeq - c.readSeq) } : c))

  const upsertConversation = (pb: PbConversation): Conversation => {
    const id = pb.id.toString()
    const prev = get().conversations[id]
    const next = mergeWithLocal(toConversation(pb, prev), prev)
    set((s) => ({
      conversations: { ...s.conversations, [id]: next },
      users: pb.peerUser ? { ...s.users, [pb.peerUser.id.toString()]: toChatUser(pb.peerUser) } : s.users,
    }))
    return next
  }

  const ensureConversation = (id: string): Promise<Conversation | undefined> => {
    const running = conversationFetches.get(id)
    if (running) return running
    const p = api
      .getConversation(BigInt(id))
      .then((pb) => (pb && !disposed ? upsertConversation(pb) : undefined))
      .catch((e: unknown) => {
        warn("get conversation failed", e, { id })
        return undefined
      })
      .finally(() => conversationFetches.delete(id))
    conversationFetches.set(id, p)
    return p
  }

  const loadLatest = (id: string, jumped = false): Promise<void> => {
    const running = latestLoads.get(id)
    if (running) return running
    const p = (async () => {
      const res = await api.listMessages(BigInt(id), 0n, PAGE_SIZE)
      if (disposed) return
      const messages = res.messages.map(toChatMessage).reverse()
      setTimeline(id, {
        messages,
        contiguousMaxSeq: messages.at(-1)?.seq ?? 0,
        hasOlder: res.nextBeforeSeq !== 0n,
        loadingOlder: false,
        jumped,
      })
      applySummary(id, messages)
    })()
      .catch((e: unknown) => warn("load messages failed", e, { id }))
      .finally(() => latestLoads.delete(id))
    latestLoads.set(id, p)
    return p
  }

  const differenceOnce = async (id: string) => {
    for (;;) {
      const t = get().timelines[id]
      if (!t) return
      const res = await api.getDifference(BigInt(id), BigInt(t.contiguousMaxSeq), DIFFERENCE_LIMIT)
      if (disposed) return
      if (res.result === DifferenceResult.TOO_LONG) {
        // 缺口过大：丢弃旧时间线，重新加载最新一页（设计 3.9.3）
        await loadLatest(id, true)
        return
      }
      const messages = res.messages.map(toChatMessage)
      const cur = get().timelines[id]
      if (!cur) return
      setTimeline(id, mergeTimeline(cur, messages))
      applySummary(id, messages)
      bumpMaxSeq(id, Number(res.maxSeq))
      fetched += messages.length
      if (messages.length > 0) deps.onProgress?.(fetched)
      // SLICE 时继续拉；下一次的 from_seq 取合并后的 contiguousMaxSeq，与 next_from_seq 一致
      if (res.result !== DifferenceResult.SLICE || messages.length === 0) return
    }
  }

  // 同一会话同时只有一个补拉；补拉期间又发现缺口，结束后再补一轮
  const requestDifference = (id: string): Promise<void> => {
    const running = differences.get(id)
    if (running) {
      differenceAgain.add(id)
      return running
    }
    const p = (async () => {
      do {
        differenceAgain.delete(id)
        await differenceOnce(id)
      } while (differenceAgain.has(id) && !disposed)
    })()
      .catch((e: unknown) => warn("get difference failed", e, { id }))
      .finally(() => differences.delete(id))
    differences.set(id, p)
    return p
  }

  const syncIfBehind = (id: string) => {
    const t = get().timelines[id]
    const c = get().conversations[id]
    if (t && c && c.maxSeq > t.contiguousMaxSeq) void requestDifference(id)
  }

  const computeMarker = (id: string) => {
    const s = get()
    const c = s.conversations[id]
    const t = s.timelines[id]
    if (!c || !t) return
    const first = c.unread > 0 ? t.messages.find((m) => m.senderId !== s.meId && (m.seq ?? 0) > c.readSeq) : undefined
    set((st) => ({ unreadMarker: { ...st.unreadMarker, [id]: first?.id } }))
  }

  const loadMembers = async (groupId: string) => {
    if (membersLoaded.has(groupId)) return
    membersLoaded.add(groupId)
    try {
      let cursor = 0n
      do {
        const res = await api.listGroupMembers(BigInt(groupId), cursor, MEMBER_PAGE)
        if (disposed) return
        set((s) => {
          const users = { ...s.users }
          for (const m of res.members) if (m.profile) users[m.userId.toString()] = toChatUser(m.profile)
          return { users }
        })
        cursor = res.nextCursor
      } while (cursor !== 0n)
    } catch (e) {
      // 下次打开再试；成员资料缺失时名称显示「用户 <ID>」
      membersLoaded.delete(groupId)
      warn("list group members failed", e, { groupId })
    }
  }

  const flushRead = async (id: string) => {
    readTimers.delete(id)
    const s = get()
    const c = s.conversations[id]
    const seq = s.timelines[id]?.messages.at(-1)?.seq ?? 0
    if (!c || seq <= c.readSeq) return
    patchConversation(id, (cur) => ({ ...cur, readSeq: maxOf(cur.readSeq, seq), unread: Math.max(0, cur.maxSeq - maxOf(cur.readSeq, seq)) }))
    try {
      const readSeq = Number(await api.markRead(BigInt(id), BigInt(seq)))
      patchConversation(id, (cur) =>
        readSeq > cur.readSeq ? { ...cur, readSeq, unread: Math.max(0, cur.maxSeq - readSeq) } : cur,
      )
    } catch (e) {
      // 本地已读位置先保留；其他设备的未读数以服务端为准，下次重连同步时校正
      warn("mark read failed", e, { id, seq })
    }
  }

  return {
    syncAll: async () => {
      fetched = 0
      const list: PbConversation[] = []
      let cursor = 0n
      do {
        const res = await api.listConversations(cursor, CONVERSATION_PAGE)
        list.push(...res.conversations)
        cursor = res.nextCursor
      } while (cursor !== 0n)
      if (disposed) return

      set((s) => {
        const conversations: Record<string, Conversation> = {}
        const users = { ...s.users }
        for (const pb of list) {
          const id = pb.id.toString()
          conversations[id] = mergeWithLocal(toConversation(pb, s.conversations[id]), s.conversations[id])
          if (pb.peerUser) users[pb.peerUser.id.toString()] = toChatUser(pb.peerUser)
        }
        // 不在列表里的会话（退群后看不到）连同时间线一起删除
        const timelines = Object.fromEntries(Object.entries(s.timelines).filter(([id]) => conversations[id]))
        return { conversations, users, timelines, listLoaded: true }
      })
      await Promise.all(
        Object.keys(get().timelines)
          .filter((id) => get().conversations[id].maxSeq > get().timelines[id].contiguousMaxSeq)
          .map((id) => requestDifference(id)),
      )
    },

    handleMessage: (pb) => {
      const m = toChatMessage(pb)
      const id = m.conversationId
      if (!get().conversations[id]) {
        // 不认识的会话（新建的群、别人第一次发来的单聊）：会话信息里已经带着这条消息
        void ensureConversation(id)
        return
      }
      applySummary(id, [m])
      const t = get().timelines[id]
      if (!t || (m.seq ?? 0) <= t.contiguousMaxSeq) return
      const next = mergeTimeline(t, [m])
      setTimeline(id, next)
      if (next.contiguousMaxSeq < (m.seq ?? 0)) void requestDifference(id)
    },

    handleRead: (r) => {
      const id = r.conversationId.toString()
      const readSeq = Number(r.readSeq)
      if (r.userId.toString() === get().meId) {
        patchConversation(id, (c) =>
          readSeq > c.readSeq ? { ...c, readSeq, unread: Math.max(0, c.maxSeq - readSeq) } : c,
        )
      } else {
        patchConversation(id, (c) => (readSeq > c.peerReadSeq ? { ...c, peerReadSeq: readSeq } : c))
      }
    },

    confirmSent: (local, ack) => {
      const id = ack.conversationId.toString()
      const seq = Number(ack.seq)
      const confirmed: ChatMessage = {
        ...local,
        conversationId: id,
        seq,
        sentAt: ack.createdAt ? timestampMs(ack.createdAt) : local.sentAt,
        status: "sent",
        failReason: undefined,
      }
      set((s) => removeFromOutbox(s.outbox, local.id))

      if (!get().conversations[id]) {
        // 草稿单聊的第一条消息：服务端刚创建会话。先用本地信息建一个会话让界面立即切换，再取完整信息
        const peerId = local.conversationId.startsWith(draftKey("")) ? local.conversationId.slice(draftKey("").length) : undefined
        const peer = peerId ? get().users[peerId] : undefined
        set((s) => ({
          conversations: {
            ...s.conversations,
            [id]: {
              id,
              kind: "single",
              peerId,
              title: peer?.nickname ?? `用户 ${peerId ?? id}`,
              maxSeq: seq,
              readSeq: seq,
              peerReadSeq: 0,
              unread: 0,
              last: confirmed,
            },
          },
          timelines: {
            ...s.timelines,
            [id]: { messages: [confirmed], contiguousMaxSeq: seq === 1 ? 1 : 0, hasOlder: seq > 1, loadingOlder: false, jumped: false },
          },
          draftResolved: peerId ? { ...s.draftResolved, [peerId]: id } : s.draftResolved,
          // 草稿里还没确认的消息挂到新会话下
          outbox: peerId ? moveOutbox(s.outbox, draftKey(peerId), id) : s.outbox,
        }))
        void ensureConversation(id).then(() => syncIfBehind(id))
        return
      }

      applySummary(id, [confirmed])
      const t = get().timelines[id]
      if (!t || seq <= t.contiguousMaxSeq) return
      const next = mergeTimeline(t, [confirmed])
      setTimeline(id, next)
      if (next.contiguousMaxSeq < seq) void requestDifference(id)
    },

    open: (id, force = false) => {
      // StrictMode 下 effect 会执行两次，同一个会话重复打开时不能重新计算分隔线，返回进行中的那一次
      if (id === get().activeId && !force) return opening
      // 先去掉这个会话的分隔线记录：界面在记录写回（computeMarker）之后才定位时间线
      set((s) => {
        const unreadMarker = { ...s.unreadMarker }
        if (id) delete unreadMarker[id]
        return { activeId: id, unreadMarker }
      })
      if (!id) return (opening = Promise.resolve())
      opening = (async () => {
        const c = get().conversations[id] ?? (await ensureConversation(id))
        if (!c || disposed) return
        if (!get().timelines[id]) await loadLatest(id)
        syncIfBehind(id)
        computeMarker(id)
        if (c.kind === "group") void loadMembers(id)
      })()
      return opening
    },

    loadOlder: async (id) => {
      const t = get().timelines[id]
      if (!t || !t.hasOlder || t.loadingOlder || t.messages.length === 0) return
      setTimeline(id, { ...t, loadingOlder: true })
      try {
        const res = await api.listMessages(BigInt(id), BigInt(t.messages[0].seq ?? 0), PAGE_SIZE)
        const cur = get().timelines[id]
        if (!cur || disposed) return
        const merged = mergeTimeline(cur, res.messages.map(toChatMessage))
        setTimeline(id, { ...merged, hasOlder: res.nextBeforeSeq !== 0n, loadingOlder: false })
      } catch (e) {
        warn("load older messages failed", e, { id })
        const cur = get().timelines[id]
        if (cur) setTimeline(id, { ...cur, loadingOlder: false })
      }
    },

    reportRead: (id) => {
      clearTimeout(readTimers.get(id))
      readTimers.set(
        id,
        setTimeout(() => void flushRead(id), READ_DEBOUNCE_MS),
      )
    },

    ensureConversation,

    removeConversation: (id) => {
      membersLoaded.delete(id)
      set((s) => {
        if (!s.conversations[id]) return {}
        const conversations = { ...s.conversations }
        const timelines = { ...s.timelines }
        const unreadMarker = { ...s.unreadMarker }
        delete conversations[id]
        delete timelines[id]
        delete unreadMarker[id]
        return { conversations, timelines, unreadMarker }
      })
    },

    setBlocked: (id, reason) =>
      patchConversation(id, (c) => (c.blockedReason === reason ? c : { ...c, blockedReason: reason })),

    dispose: () => {
      disposed = true
      readTimers.forEach(clearTimeout)
      readTimers.clear()
    },
  }
}

// 服务端的会话摘要和本地合并：seq 与已读位置只增不减，本地可能已经通过推送拿到了更新的值
function mergeWithLocal(next: Conversation, prev: Conversation | undefined): Conversation {
  if (!prev) return next
  const maxSeq = maxOf(next.maxSeq, prev.maxSeq)
  const readSeq = maxOf(next.readSeq, prev.readSeq)
  const last = (prev.last?.seq ?? 0) > (next.last?.seq ?? 0) ? prev.last : next.last
  return {
    ...next,
    maxSeq,
    readSeq,
    peerReadSeq: maxOf(next.peerReadSeq, prev.peerReadSeq),
    unread: Math.max(0, maxSeq - readSeq),
    last,
  }
}

function removeFromOutbox(outbox: Record<string, ChatMessage[]>, messageId: string): { outbox: Record<string, ChatMessage[]> } {
  const next: Record<string, ChatMessage[]> = {}
  for (const [key, list] of Object.entries(outbox)) {
    const rest = list.filter((m) => m.id !== messageId)
    if (rest.length > 0) next[key] = rest
  }
  return { outbox: next }
}

function moveOutbox(outbox: Record<string, ChatMessage[]>, from: string, to: string): Record<string, ChatMessage[]> {
  const moving = outbox[from]
  if (!moving) return outbox
  const next = { ...outbox }
  delete next[from]
  next[to] = [...(next[to] ?? []), ...moving.map((m) => ({ ...m, conversationId: to }))]
  return next
}
