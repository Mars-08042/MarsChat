// 浏览器中的实时客户端：把连接、同步引擎、发送队列接到 WebSocket、HTTP 接口和聊天 store 上，
// 并提供界面调用的动作。登录后由主框架启动，退出或登录状态结束时停止。

import { api, tokens } from "@/api/client"
import {
  getConversation,
  getDifference,
  getUser,
  listConversations,
  listFriends,
  listGroupMembers,
  listMessages,
  markRead,
} from "@/api/endpoints"
import { isApiError } from "@/api/errors"
import type { EndReason } from "@/api/tokens"
import type { ConnectionStatus } from "@/components/common/ConnectionPill"
import type { ServerFrame } from "@/gen/access/v1/frame_pb"
import { RevokeReason } from "@/gen/account/v1/events_pb"
import { ConversationType } from "@/gen/message/v1/message_pb"
import type { ChatMessage, ChatUser, Conversation } from "@/features/chat/types"
import { chatStore, draftKey, initialChatState } from "@/stores/chat"
import { SUBPROTOCOL } from "./codec"
import { createConnection, type Connection, type ConnectionState, type OpenSocket } from "./connection"
import { messageId, toChatUser } from "./convert"
import { createSendQueue, type Outgoing, type SendQueue } from "./send-queue"
import { createSyncEngine, type SyncApi, type SyncEngine } from "./sync"

const log = {
  warn: (message: string, detail?: Record<string, unknown>) => console.warn(`[realtime] ${message}`, detail ?? ""),
  error: (message: string, detail?: Record<string, unknown>) => console.error(`[realtime] ${message}`, detail ?? ""),
}

const syncApi: SyncApi = {
  listConversations: (cursor, limit) => api.send(listConversations, { cursor, limit }),
  getConversation: async (id) => {
    try {
      return (await api.send(getConversation, { conversationId: id })).conversation
    } catch (e) {
      // 不是成员时一律返回 CONVERSATION_NOT_FOUND
      if (isApiError(e) && e.reason === "CONVERSATION_NOT_FOUND") return undefined
      throw e
    }
  },
  listMessages: (id, beforeSeq, limit) => api.send(listMessages, { conversationId: id, beforeSeq, limit }),
  getDifference: (id, fromSeq, limit) => api.send(getDifference, { conversationId: id, fromSeq, limit }),
  markRead: async (id, seq) => (await api.send(markRead, { conversationId: id, seq })).readSeq,
  listGroupMembers: (groupId, cursor, limit) => api.send(listGroupMembers, { groupId, cursor, limit }),
}

const openSocket: OpenSocket = (h) => {
  const scheme = location.protocol === "https:" ? "wss" : "ws"
  const ws = new WebSocket(`${scheme}://${location.host}/ws`, [SUBPROTOCOL])
  ws.binaryType = "arraybuffer"
  ws.onopen = () => h.open()
  ws.onmessage = (e: MessageEvent) => {
    if (e.data instanceof ArrayBuffer) h.message(e.data)
  }
  ws.onclose = (e) => h.close(e.code, e.reason)
  return { send: (data) => ws.send(data), close: (code, reason) => ws.close(code, reason) }
}

// 被踢下线的原因（RevokeReason）对应的登录状态结束原因。LOGOUT 是这个会话自己退出（同一浏览器的其他标签页），不提示
const KICK_REASONS: Record<RevokeReason, EndReason> = {
  [RevokeReason.UNSPECIFIED]: "revoked",
  [RevokeReason.LOGOUT]: "logout",
  [RevokeReason.KICKED]: "kicked",
  [RevokeReason.BANNED]: "banned",
  [RevokeReason.REPLACED]: "replaced",
  [RevokeReason.REFRESH_REUSE]: "reuse",
}

interface Instance {
  conn: Connection
  engine: SyncEngine
  queue: SendQueue
}

let current: Instance | null = null

const patch = (p: Partial<ReturnType<typeof chatStore.getState>>) => chatStore.setState(p)

function replaceInOutbox(message: ChatMessage) {
  chatStore.setState((s) => {
    for (const [key, list] of Object.entries(s.outbox)) {
      const i = list.findIndex((m) => m.id === message.id)
      if (i >= 0) return { outbox: { ...s.outbox, [key]: list.map((m, j) => (j === i ? message : m)) } }
    }
    const key = message.conversationId
    return { outbox: { ...s.outbox, [key]: [...(s.outbox[key] ?? []), message] } }
  })
}

export interface StopOptions {
  // 登录状态被动结束（被踢、过期）时保留聊天数据，作为失效提示的背景；下次启动时重置
  keepData?: boolean
}

// 启动实时客户端，返回停止函数
export function startRealtime(meId: string): (options?: StopOptions) => void {
  current?.conn.stop()
  chatStore.setState(initialChatState(meId), true)

  // 本轮同步是否完成；完成前胶囊显示连接中或补拉进度
  let syncing = false
  const setConnection = (connection: ConnectionStatus) => patch({ connection })

  const engine = createSyncEngine({
    store: chatStore,
    api: syncApi,
    onProgress: (count) => {
      if (syncing) setConnection({ kind: "syncing", count })
    },
    log,
  })

  const queue = createSendQueue({
    send: (body) => conn.send(body),
    onUpdate: replaceInOutbox,
    onAck: (message, ack) => engine.confirmSent(message, ack),
    onForbidden: (message, _reason, text) => {
      const id = message.conversationId
      chatStore.setState((s) =>
        s.conversations[id] ? { conversations: { ...s.conversations, [id]: { ...s.conversations[id], blockedReason: text } } } : {},
      )
    },
    onRateLimited: (until) => patch({ rateLimitedUntil: until }),
  })

  const onState = (state: ConnectionState) => {
    switch (state.kind) {
      case "connecting":
      case "idle":
        syncing = false
        setConnection({ kind: "connecting" })
        return
      case "open":
        syncing = true
        setConnection({ kind: "connecting" })
        queue.connected()
        engine.syncAll().then(
          () => {
            syncing = false
            if (conn.state().kind === "open") setConnection({ kind: "synced" })
          },
          (e: unknown) => {
            // 会话列表拉取失败：保持连接，推送照常处理；下次重连再完整同步
            syncing = false
            log.warn("sync failed", { error: String(e) })
            if (conn.state().kind === "open") setConnection({ kind: "synced" })
          },
        )
        return
      case "waiting":
        syncing = false
        queue.disconnected()
        setConnection(state.maintenance ? { kind: "maintenance" } : { kind: "reconnecting", retryAt: state.retryAt })
        return
      case "offline":
        syncing = false
        queue.disconnected()
        setConnection({ kind: "offline" })
        return
      case "kicked":
        syncing = false
        queue.disconnected()
    }
  }

  const onFrame = (frame: ServerFrame) => {
    const body = frame.body
    switch (body.case) {
      case "message":
        engine.handleMessage(body.value)
        return
      case "read":
        engine.handleRead(body.value)
        return
      case "sendAck":
        queue.handleAck(frame.requestId, body.value)
        return
      case "error":
        if (frame.requestId === 0n || !queue.handleError(frame.requestId, body.value)) {
          log.warn("server error frame", { requestId: frame.requestId.toString(), reason: body.value.reason })
        }
        return
      // 好友与群通知在第 5 阶段处理，通话在第 8 阶段接入
      default:
        return
    }
  }

  const onKicked = (reason: RevokeReason, token: string) => {
    // 同一浏览器在其他标签页重新登录时，旧会话被替换；本地已经是新会话，直接用新令牌重连
    const latest = tokens.current()
    if (reason === RevokeReason.REPLACED && latest && latest.accessToken !== token) {
      conn.stop()
      conn.start()
      return
    }
    tokens.end(KICK_REASONS[reason] ?? "revoked")
  }

  const conn = createConnection({
    openSocket,
    accessToken: () => tokens.accessToken(),
    refresh: (stale) => tokens.refresh(stale),
    isOnline: () => navigator.onLine,
    onState,
    onFrame,
    onKicked,
    log,
  })

  const online = () => conn.setOnline(true)
  const offline = () => conn.setOnline(false)
  // 还有没确认的消息时离开页面要确认：发送队列只在内存里（设计 3.9.4）
  const beforeUnload = (e: BeforeUnloadEvent) => {
    if (queue.hasUnconfirmed()) e.preventDefault()
  }
  window.addEventListener("online", online)
  window.addEventListener("offline", offline)
  window.addEventListener("beforeunload", beforeUnload)

  const instance: Instance = { conn, engine, queue }
  current = instance
  conn.start()

  return (options = {}) => {
    window.removeEventListener("online", online)
    window.removeEventListener("offline", offline)
    window.removeEventListener("beforeunload", beforeUnload)
    conn.stop()
    engine.dispose()
    queue.clear()
    if (current === instance) {
      current = null
      if (!options.keepData) chatStore.setState(initialChatState(), true)
    }
  }
}

// 发送目标：已有会话，或者和好友的草稿单聊
export type SendTarget = Conversation | { draftPeerId: string }

// 好友列表最多取这么多页，用于新建会话；好友上限远小于这个数
const FRIEND_PAGES = 20
const FRIEND_PAGE_SIZE = 100

// 界面调用的动作。实时客户端没有启动时（未登录）什么也不做
export const chat = {
  open: (conversationId: string | undefined, force = false) =>
    current?.engine.open(conversationId, force) ?? Promise.resolve(),
  loadOlder: (conversationId: string) => current?.engine.loadOlder(conversationId),
  reportRead: (conversationId: string) => current?.engine.reportRead(conversationId),
  retryNow: () => current?.conn.retryNow(),
  retry: (messageId: string) => current?.queue.retry(messageId),

  // 新建会话时选择好友；资料同时写入 users，草稿单聊的标题和头像从这里取
  loadFriends: async (): Promise<ChatUser[]> => {
    const friends: ChatUser[] = []
    let cursor = 0n
    for (let page = 0; page < FRIEND_PAGES; page++) {
      const res = await api.send(listFriends, { cursor, limit: FRIEND_PAGE_SIZE })
      for (const f of res.friends) if (f.profile) friends.push(toChatUser(f.profile))
      cursor = res.nextCursor
      if (cursor === 0n) break
    }
    chatStore.setState((s) => {
      const users = { ...s.users }
      for (const u of friends) users[u.id] = u
      return { users }
    })
    return friends
  },

  // 草稿单聊从地址栏直接打开时取对方资料；失败时标题保持「用户 <ID>」
  loadUser: async (userId: string) => {
    try {
      const { user } = await api.send(getUser, { userId: BigInt(userId) })
      if (user) chatStore.setState((s) => ({ users: { ...s.users, [userId]: toChatUser(user) } }))
    } catch (e) {
      log.warn("get user failed", { userId, error: String(e) })
    }
  },

  sendText: (target: SendTarget, text: string) => {
    if (!current) return
    const meId = chatStore.getState().meId
    const clientMsgId = crypto.randomUUID()
    let key: string
    let outgoing: Outgoing
    const content = { body: { case: "text" as const, value: { text } } }
    if ("draftPeerId" in target) {
      key = draftKey(target.draftPeerId)
      outgoing = { peerType: ConversationType.SINGLE, peerId: BigInt(target.draftPeerId), clientMsgId, content }
    } else if (target.kind === "single" && target.peerId) {
      key = target.id
      outgoing = { peerType: ConversationType.SINGLE, peerId: BigInt(target.peerId), clientMsgId, content }
    } else {
      // 群聊会话 ID 即群 ID
      key = target.id
      outgoing = { peerType: ConversationType.GROUP, peerId: BigInt(target.id), clientMsgId, content }
    }
    const message: ChatMessage = {
      id: messageId(meId, clientMsgId),
      conversationId: key,
      senderId: meId,
      content: { kind: "text", text },
      sentAt: Date.now(),
      status: "pending",
    }
    current.queue.enqueue(message, outgoing)
  },
}
