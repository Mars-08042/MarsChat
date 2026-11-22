// 视觉原型阶段的聊天状态：用定时器模拟上传进度、SendAck、已读回执和连接状态变化。
// 演示数据已删除，初始没有会话；第 3 阶段登录后写入当前用户，第 4 阶段由 realtime/ 的同步引擎与发送队列取代，
// 第 7 阶段的上传换成 XMLHttpRequest。

import { create } from "zustand"
import type { LocalFile } from "./local-file"
import type { ConnectionStatus } from "@/components/common/ConnectionPill"
import { planChatUpload, type UploadPlan } from "./files"
import type { ChatMessage, ChatUser, Conversation, MessageContent } from "./types"

const ACK_DELAY_MS = 650
const PEER_READ_DELAY_MS = 1600
// 模拟的上传速度，100 MB 大约 12 秒
const UPLOAD_BYTES_PER_SECOND = 8 * 1024 * 1024
const UPLOAD_TICK_MS = 100
// 未登录时的当前用户：空 ID 不会和任何消息的发送者相同
const NOBODY: ChatUser = { id: "", nickname: "" }

interface ChatStore {
  me: ChatUser
  users: Record<string, ChatUser>
  conversations: Record<string, Conversation>
  messages: Record<string, ChatMessage[]>
  // 打开会话时第一条未读消息的 ID，用来插入「以下为新消息」
  unreadMarker: Record<string, string | undefined>
  activeId?: string
  connection: ConnectionStatus

  open: (conversationId: string | undefined) => void
  send: (conversationId: string, text: string) => void
  upload: (conversationId: string, file: LocalFile) => UploadPlan
  cancelUpload: (conversationId: string, messageId: string) => void
  retry: (conversationId: string, messageId: string) => void
  setConnection: (status: ConnectionStatus) => void
  playReconnect: () => void
}

const lastSeq = (messages: ChatMessage[]) => messages.reduce((max, m) => Math.max(max, m.seq ?? 0), 0)
const randomId = () => String(Math.floor(Math.random() * 9e15) + 1e15)

// 演示脚本的定时器；切换连接状态时全部取消
let connectionTimers: ReturnType<typeof setTimeout>[] = []
function clearConnectionTimers() {
  connectionTimers.forEach(clearTimeout)
  connectionTimers = []
}

// 进行中的模拟上传，键为消息 ID
const uploadTimers = new Map<string, ReturnType<typeof setInterval>>()
function stopUpload(messageId: string) {
  clearInterval(uploadTimers.get(messageId))
  uploadTimers.delete(messageId)
}

function fileSize(content: MessageContent): number {
  if (content.kind === "image") return content.image.size
  if (content.kind === "file") return content.file.size
  return 0
}

// 上传完成后服务端返回 file_id 和 access_key，写回消息内容
function withUploadedIds(content: MessageContent): MessageContent {
  const ids = { fileId: randomId(), accessKey: `ak_${randomId()}` }
  if (content.kind === "image") return { kind: "image", image: { ...content.image, ...ids } }
  if (content.kind === "file") return { kind: "file", file: { ...content.file, ...ids } }
  return content
}

export const useChatStore = create<ChatStore>()((set, get) => {
  const findMessage = (conversationId: string, messageId: string) =>
    get().messages[conversationId]?.find((m) => m.id === messageId)

  const updateMessage = (conversationId: string, messageId: string, patch: Partial<ChatMessage>) =>
    set((s) => ({
      messages: {
        ...s.messages,
        [conversationId]: s.messages[conversationId].map((m) => (m.id === messageId ? { ...m, ...patch } : m)),
      },
    }))

  const appendMessage = (message: ChatMessage) =>
    set((s) => ({
      messages: { ...s.messages, [message.conversationId]: [...(s.messages[message.conversationId] ?? []), message] },
    }))

  const updateConversation = (conversationId: string, patch: Partial<Conversation>) =>
    set((s) => ({
      conversations: { ...s.conversations, [conversationId]: { ...s.conversations[conversationId], ...patch } },
    }))

  // 模拟发送队列：等待 SendAck，成功后分配 seq；单聊再模拟对方已读
  const deliver = (conversationId: string, messageId: string) => {
    setTimeout(() => {
      const conversation = get().conversations[conversationId]
      const seq = lastSeq(get().messages[conversationId]) + 1
      updateMessage(conversationId, messageId, { status: "sent", seq, failReason: undefined })
      updateConversation(conversationId, { readSeq: seq })
      if (conversation.kind === "single") {
        setTimeout(() => updateConversation(conversationId, { peerReadSeq: seq }), PEER_READ_DELAY_MS)
      }
    }, ACK_DELAY_MS)
  }

  // 模拟上传：按固定速度推进进度，完成后拿到文件 ID，再进入发送队列
  const startUpload = (conversationId: string, messageId: string) => {
    const message = findMessage(conversationId, messageId)
    if (!message) return
    const total = Math.max(1, fileSize(message.content))
    let progress = 0
    updateMessage(conversationId, messageId, { status: "uploading", upload: { progress }, failReason: undefined })

    stopUpload(messageId)
    uploadTimers.set(
      messageId,
      setInterval(() => {
        progress = Math.min(1, progress + (UPLOAD_BYTES_PER_SECOND * UPLOAD_TICK_MS) / 1000 / total)
        if (progress < 1) {
          updateMessage(conversationId, messageId, { upload: { progress } })
          return
        }
        stopUpload(messageId)
        const current = findMessage(conversationId, messageId)
        if (!current) return
        updateMessage(conversationId, messageId, {
          status: "pending",
          upload: undefined,
          content: withUploadedIds(current.content),
        })
        deliver(conversationId, messageId)
      }, UPLOAD_TICK_MS),
    )
  }

  return {
    me: NOBODY,
    users: {},
    conversations: {},
    messages: {},
    unreadMarker: {},
    activeId: undefined,
    connection: { kind: "synced" },

    open: (conversationId) => {
      // StrictMode 下 effect 会执行两次，同一个会话重复打开时不能清掉分隔线
      if (conversationId === get().activeId) return
      set({ activeId: conversationId })
      if (!conversationId) return
      const conversation = get().conversations[conversationId]
      if (!conversation) return
      const messages = get().messages[conversationId] ?? []
      const firstUnread =
        conversation.unread > 0
          ? messages.find((m) => m.senderId !== get().me.id && (m.seq ?? 0) > conversation.readSeq)?.id
          : undefined
      set((s) => ({ unreadMarker: { ...s.unreadMarker, [conversationId]: firstUnread } }))
      updateConversation(conversationId, { unread: 0, readSeq: lastSeq(messages) })
    },

    send: (conversationId, text) => {
      const message: ChatMessage = {
        id: crypto.randomUUID(),
        conversationId,
        senderId: get().me.id,
        content: { kind: "text", text },
        sentAt: Date.now(),
        status: "pending",
      }
      appendMessage(message)
      deliver(conversationId, message.id)
    },

    upload: (conversationId, file) => {
      const pixels = file.width && file.height ? file.width * file.height : undefined
      const plan = planChatUpload(file, pixels)
      if (!plan.ok) return plan
      // 上传前还没有文件 ID；名称、大小、类型先用本地值，服务端返回后以服务端为准
      const meta = { fileId: "", accessKey: "", name: file.name, size: file.size, mimeType: file.type || "application/octet-stream" }
      const content: MessageContent =
        plan.as === "image" && file.width && file.height
          ? { kind: "image", image: { ...meta, width: file.width, height: file.height } }
          : { kind: "file", file: meta }
      const message: ChatMessage = {
        id: crypto.randomUUID(),
        conversationId,
        senderId: get().me.id,
        content,
        sentAt: Date.now(),
        status: "uploading",
        upload: { progress: 0 },
        localUrl: content.kind === "image" ? file.url : undefined,
      }
      appendMessage(message)
      startUpload(conversationId, message.id)
      return plan
    },

    cancelUpload: (conversationId, messageId) => {
      stopUpload(messageId)
      const message = findMessage(conversationId, messageId)
      if (message?.localUrl?.startsWith("blob:")) URL.revokeObjectURL(message.localUrl)
      set((s) => ({
        messages: { ...s.messages, [conversationId]: s.messages[conversationId].filter((m) => m.id !== messageId) },
      }))
    },

    retry: (conversationId, messageId) => {
      const message = findMessage(conversationId, messageId)
      if (!message) return
      // 没拿到文件 ID 说明是上传失败，重新上传；否则沿用同一个 client_msg_id 重新发送
      const uploaded =
        message.content.kind === "text" ||
        (message.content.kind === "image" ? message.content.image.fileId : message.content.file.fileId) !== ""
      if (!uploaded) {
        startUpload(conversationId, messageId)
        return
      }
      updateMessage(conversationId, messageId, { status: "pending", failReason: undefined })
      deliver(conversationId, messageId)
    },

    setConnection: (status) => {
      clearConnectionTimers()
      set({ connection: status })
    },

    // 演示完整的断线重连：等待 3 秒 → 连接 → 补拉 23 条 → 已同步
    playReconnect: () => {
      clearConnectionTimers()
      const at = (ms: number, status: ConnectionStatus) =>
        connectionTimers.push(setTimeout(() => set({ connection: status }), ms))
      set({ connection: { kind: "reconnecting", retryAt: Date.now() + 3000 } })
      at(3000, { kind: "connecting" })
      const total = 23
      for (let i = 1; i <= 6; i++) at(3800 + i * 180, { kind: "syncing", count: Math.round((total * i) / 6) })
      at(3800 + 7 * 180 + 300, { kind: "synced" })
    },
  }
})
