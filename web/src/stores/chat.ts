// 聊天状态（设计 3.9.3）：由 realtime/ 的同步引擎和发送队列写入，界面只读。
// 用 zustand 的 vanilla store，同步引擎和单元测试不依赖 React。

import { useStore } from "zustand"
import { createStore, type StoreApi } from "zustand/vanilla"
import type { ConnectionStatus } from "@/components/common/ConnectionPill"
import type { ChatMessage, ChatUser, Conversation } from "@/features/chat/types"

export interface Timeline {
  // 已确认的消息，按 seq 升序、不重复
  messages: ChatMessage[]
  // 本地连续拿到的最大 seq。推送的 seq 比它大 1 以上说明中间有缺口，要补拉
  contiguousMaxSeq: number
  hasOlder: boolean
  loadingOlder: boolean
  // 缺口过大（TOO_LONG）时丢弃了旧的时间线，直接加载最新一页
  jumped: boolean
}

export interface ChatState {
  meId: string
  users: Record<string, ChatUser>
  conversations: Record<string, Conversation>
  // 是否已经拿到过会话列表；拿到之前列表显示加载中，而不是「还没有会话」
  listLoaded: boolean
  // 只有打开过的会话才有时间线
  timelines: Record<string, Timeline>
  // 自己发出、还没确认的消息（发送中、失败），键为会话 ID 或草稿单聊的 draftKey
  outbox: Record<string, ChatMessage[]>
  // 打开会话时第一条未读消息的 ID，用来插入「以下为新消息」
  unreadMarker: Record<string, string | undefined>
  // 草稿单聊的第一条消息确认后得到的会话 ID，键为对方的用户 ID
  draftResolved: Record<string, string>
  activeId?: string
  connection: ConnectionStatus
  // 发消息被限流时，自动重发要等到这个时间（毫秒时间戳）
  rateLimitedUntil?: number
}

export type ChatStore = StoreApi<ChatState>

// 和好友还没有会话时，发出的消息先挂在这个键下（设计 3.5 的 /chat/with/:userId）
export const draftKey = (userId: string) => `with:${userId}`

export const initialChatState = (meId = ""): ChatState => ({
  meId,
  users: {},
  conversations: {},
  listLoaded: false,
  timelines: {},
  outbox: {},
  unreadMarker: {},
  draftResolved: {},
  activeId: undefined,
  connection: { kind: "connecting" },
  rateLimitedUntil: undefined,
})

export const createChatStore = (): ChatStore => createStore<ChatState>()(() => initialChatState())

export const chatStore = createChatStore()

export function useChat<T>(selector: (s: ChatState) => T): T {
  return useStore(chatStore, selector)
}
