// 浏览器中的实时客户端：把连接、同步引擎、发送队列接到 WebSocket、HTTP 接口和聊天 store 上，
// 并提供界面调用的动作。登录后由主框架启动，退出或登录状态结束时停止。

import { toast } from "sonner"
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
import { isApiError, reasonText } from "@/api/errors"
import type { EndReason } from "@/api/tokens"
import type { ConnectionStatus } from "@/components/common/ConnectionPill"
import type { Notification, ServerFrame } from "@/gen/access/v1/frame_pb"
import { RevokeReason } from "@/gen/account/v1/events_pb"
import { ConversationType } from "@/gen/message/v1/message_pb"
import type { ChatMessage, ChatUser, Conversation } from "@/features/chat/types"
import { groupBlock, type GroupInfo, type MemberItem } from "@/features/groups/model"
import { groupQuery, membersQuery } from "@/features/groups/queries"
import { queryClient, queryKeys } from "@/lib/query"
import { formatMuteEnd } from "@/lib/time"
import { chatStore, draftKey, initialChatState } from "@/stores/chat"
import { useUi } from "@/stores/ui"
import { SUBPROTOCOL } from "./codec"
import { createConnection, type Connection, type ConnectionState, type OpenSocket } from "./connection"
import { GROUP_DISMISSED_TEXT, messageId, toChatUser } from "./convert"
import { createDeduper, namesNeeded, notificationEffects, type NotificationEffects, type SelfMember, type ToastAction } from "./notifications"
import { createSendQueue, type Outgoing, type SendQueue } from "./send-queue"
import { createSyncEngine, type SyncApi, type SyncEngine } from "./sync"

// 开发环境：这个模块持有当前连接实例，热替换后组件拿到新模块（实例为空），连接还在旧模块里，
// 打开会话、退群等动作会静默失效。它和它依赖的同步引擎、连接状态机改动时整页刷新
if (import.meta.hot) import.meta.hot.accept(() => location.reload())

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
  recheckGroup: (groupId: string) => Promise<void>
  forgetConversation: (conversationId: string) => void
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

// 通知提示上的按钮：跳到对应页面，必要时展开群面板。跳转函数由主框架注册，避免实时客户端引用路由模块
function runToastAction(action: ToastAction) {
  const ui = useUi.getState()
  ui.navigate?.(action.to)
  if (action.panel) ui.openPanel(action.to.slice("/chat/".length), action.panel)
}

function showToast(t: NonNullable<NotificationEffects["toast"]>) {
  toast(t.title, {
    description: t.description,
    // 带按钮的提示（好友申请、入群申请）多停留一会儿，来得及点
    duration: t.action ? 8000 : 5000,
    action: t.action ? { label: t.action.label, onClick: () => runToastAction(t.action as ToastAction) } : undefined,
  })
}

// 单聊会话按对方用户 ID 查找
const singleWith = (userId: string) =>
  Object.values(chatStore.getState().conversations).find((c) => c.kind === "single" && c.peerId === userId)

const NOT_FRIENDS_TEXT = reasonText("NOT_FRIENDS")

// 进群通知可能比 message 服务建好会话成员更早到达（两边分别消费 social 事件），取不到时稍后重试
const REFRESH_RETRY_MS = [500, 1500, 4000]
// 通知提示等名字的最长时间
const NAME_WAIT_MS = 1500
// setTimeout 的上限约 24.8 天，禁言最长 30 天
const MAX_TIMER_MS = 2 ** 31 - 1

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
  // 第一次连上时查询缓存是空的，之后的每次重连都要刷新
  let connectedBefore = false
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
        // 断线期间的通知不补发（data-flow 5.4）：重连后让好友、申请、群等查询全部失效，
        // 并重新计算群聊的说明条（例如断线期间被解除禁言）
        if (connectedBefore) {
          void queryClient.invalidateQueries()
          for (const c of Object.values(chatStore.getState().conversations)) {
            if (c.kind === "group" && c.blockedReason) void recheckGroup(c.id)
          }
        }
        connectedBefore = true
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
      case "notification":
        onNotification(body.value)
        return
      // 通话在第 8 阶段接入
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

  // 按群资料和自己的成员信息重新计算「不能发言」的原因：打开群聊、相关通知、重连后调用。
  // 单人禁言到期时自动再算一次，说明条随之消失
  const muteTimers = new Map<string, ReturnType<typeof setTimeout>>()
  // 自己在各群的角色和禁言截止时间，成员变更通知据此判断变化了什么。
  // 不放查询缓存：没有组件订阅的查询 5 分钟后会被回收，到时就不知道修改前的状态了
  const selfInGroup = new Map<string, SelfMember>()
  const recheckGroup = async (groupId: string) => {
    try {
      const group = await queryClient.fetchQuery(groupQuery(groupId))
      let mutedUntil: number | undefined
      if (!group.dismissed && group.myRole !== "none") {
        const members = await queryClient.fetchQuery(membersQuery(groupId))
        mutedUntil = members.find((m) => m.user.id === meId)?.mutedUntil
      }
      if (current !== instance) return
      if (group.dismissed || group.myRole === "none") selfInGroup.delete(groupId)
      else selfInGroup.set(groupId, { role: group.myRole, mutedUntil })
      const now = Date.now()
      const block = groupBlock(group, mutedUntil, now)
      clearTimeout(muteTimers.get(groupId))
      muteTimers.delete(groupId)
      if (block?.until) {
        const delay = Math.min(block.until - now + 500, MAX_TIMER_MS)
        muteTimers.set(groupId, setTimeout(() => void recheckGroup(groupId), delay))
      }
      const text = !block
        ? undefined
        : block.reason === "MEMBER_MUTED" && block.until
          ? `你已被禁言，${formatMuteEnd(block.until, now)} 解除`
          : block.reason === "GROUP_DISMISSED"
            ? GROUP_DISMISSED_TEXT
            : reasonText(block.reason)
      engine.setBlocked(groupId, text)
    } catch (e) {
      // 查不到时保持原样，下次打开或发送失败时再更新
      log.warn("recheck group failed", { groupId, error: String(e) })
    }
  }

  const refreshConversation = async (id: string) => {
    for (let i = 0; ; i++) {
      const c = await engine.ensureConversation(id)
      if (c || i >= REFRESH_RETRY_MS.length || current !== instance) return
      await new Promise((r) => setTimeout(r, REFRESH_RETRY_MS[i]))
    }
  }

  // 自己退群或被移出：从会话列表移除。正在看这个会话时回到列表，否则主区域会停在「无法打开这个会话」
  const forgetConversation = (id: string) => {
    clearTimeout(muteTimers.get(id))
    muteTimers.delete(id)
    selfInGroup.delete(id)
    engine.removeConversation(id)
    const ui = useUi.getState()
    if (ui.panel?.conversationId === id) ui.closePanel()
    if (window.location.pathname === `/chat/${id}`) ui.navigate?.("/chat")
  }

  // 提示里要用的名字本地没有时先查一次（例如陌生人的入群申请、刚被移出又被拉回的群），
  // 最多等 NAME_WAIT_MS，查不到就用通用文案
  const resolveNames = async (n: Notification) => {
    const { users, groups } = namesNeeded(n, meId)
    const known = chatStore.getState().users
    const tasks = [
      ...users.filter((id) => !known[id]).map(loadUser),
      ...groups.filter((id) => !groupName(id)).map((id) => queryClient.fetchQuery(groupQuery(id)).catch(() => undefined)),
    ]
    if (tasks.length === 0) return
    let timer: ReturnType<typeof setTimeout> | undefined
    await Promise.race([Promise.all(tasks), new Promise((r) => (timer = setTimeout(r, NAME_WAIT_MS)))])
    clearTimeout(timer)
  }

  const fresh = createDeduper()
  // 串行处理：前一条还在查名字时，后到的通知排在它后面，成员变更前后的对比才不会错位
  let notifying = Promise.resolve()
  const onNotification = (n: Notification) => {
    if (!fresh(n.eventId)) return
    notifying = notifying
      .then(async () => {
        await resolveNames(n)
        if (current === instance) applyNotification(n)
      })
      .catch((e: unknown) => log.warn("notification failed", { error: String(e) }))
  }
  const applyNotification = (n: Notification) => {
    const s = chatStore.getState()
    const effects = notificationEffects(n, {
      meId,
      userName: (id) => s.users[id]?.nickname,
      groupName,
      previousSelf: (groupId) => {
        const known = selfInGroup.get(groupId)
        if (known) return known
        const me = queryClient.getQueryData<MemberItem[]>(queryKeys.members(groupId))?.find((m) => m.user.id === meId)
        return me && { role: me.role, mutedUntil: me.mutedUntil }
      },
      now: Date.now(),
    })
    for (const queryKey of effects.invalidate) void queryClient.invalidateQueries({ queryKey })
    if (effects.selfMember) {
      const { groupId, ...self } = effects.selfMember
      selfInGroup.set(groupId, self)
    }
    if (effects.removeConversation) forgetConversation(effects.removeConversation)
    if (effects.refreshConversation) void refreshConversation(effects.refreshConversation)
    if (effects.recheckGroup) void recheckGroup(effects.recheckGroup)
    if (effects.friendship) {
      const c = singleWith(effects.friendship.userId)
      if (c && effects.friendship.friends && c.blockedReason === NOT_FRIENDS_TEXT) engine.setBlocked(c.id, undefined)
      // 已有别的原因（例如拉黑）时保留，那个原因优先
      if (c && !effects.friendship.friends && !c.blockedReason) engine.setBlocked(c.id, NOT_FRIENDS_TEXT)
    }
    if (effects.toast) showToast(effects.toast)
    if (effects.celebrate) useUi.getState().celebrate()
  }

  const instance: Instance = { conn, engine, queue, recheckGroup, forgetConversation }
  current = instance
  conn.start()

  return (options = {}) => {
    window.removeEventListener("online", online)
    window.removeEventListener("offline", offline)
    window.removeEventListener("beforeunload", beforeUnload)
    conn.stop()
    engine.dispose()
    queue.clear()
    muteTimers.forEach(clearTimeout)
    muteTimers.clear()
    if (current === instance) {
      current = null
      if (!options.keepData) chatStore.setState(initialChatState(), true)
    }
  }
}

// 取用户资料写入 users；失败时保持原样，界面显示「用户 <ID>」
async function loadUser(userId: string) {
  try {
    const { user } = await api.send(getUser, { userId: BigInt(userId) })
    if (user) chatStore.setState((s) => ({ users: { ...s.users, [userId]: toChatUser(user) } }))
  } catch (e) {
    log.warn("get user failed", { userId, error: String(e) })
  }
}

const groupName = (id: string) =>
  chatStore.getState().conversations[id]?.title ?? queryClient.getQueryData<GroupInfo>(queryKeys.group(id))?.name

// 发送目标：已有会话，或者和好友的草稿单聊
export type SendTarget = Conversation | { draftPeerId: string }

// 好友列表最多取这么多页，用于新建会话；好友上限远小于这个数
const FRIEND_PAGES = 20
const FRIEND_PAGE_SIZE = 100

// 界面调用的动作。实时客户端没有启动时（未登录）什么也不做
export const chat = {
  // 打开群聊时按群资料重新计算说明条，不必等发送失败才知道自己被禁言
  open: async (conversationId: string | undefined, force = false) => {
    const instance = current
    if (!instance) return
    await instance.engine.open(conversationId, force)
    if (conversationId && chatStore.getState().conversations[conversationId]?.kind === "group") {
      void instance.recheckGroup(conversationId)
    }
  },
  // 好友和拉黑在界面上操作成功后更新单聊的说明条；对方的变化没有通知，下次发送失败时再更新
  setFriendBlocked: (userId: string, reason: string | undefined) => {
    const c = singleWith(userId)
    if (c && current) current.engine.setBlocked(c.id, reason)
  },
  loadOlder: (conversationId: string) => current?.engine.loadOlder(conversationId),
  // 自己退群后立即从会话列表移除，不等通知
  removeConversation: (conversationId: string) => current?.forgetConversation(conversationId),
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

  // 草稿单聊从地址栏直接打开时取对方资料
  loadUser,

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
