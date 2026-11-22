// 聊天界面使用的数据结构。字段对应 message 服务的会话与消息、media 服务的文件（设计第 4 节），
// 由同步引擎（realtime/sync.ts）从接口和推送转换而来：ID 用字符串，seq 用 number（会话内连续递增，不会超出安全整数）。

export interface ChatUser {
  id: string
  nickname: string
  // media 的头像文件 ID 换到的地址，第 7 阶段接入
  avatarUrl?: string
  // 资料卡显示；未设置时为空
  gender?: "male" | "female"
  signature?: string
}

export interface Conversation {
  id: string
  kind: "single" | "group"
  title: string
  avatarUrl?: string
  // 单聊对方的用户 ID
  peerId?: string
  memberCount?: number
  // 群已解散：头部显示「已解散」而不是成员数（解散后成员数为 0）
  dismissed?: boolean
  // 会话最新消息的 seq
  maxSeq: number
  // 最新一条消息，用于会话列表的摘要和排序；还没有消息时为空
  last?: ChatMessage
  // 我已读到的 seq
  readSeq: number
  // 对方已读到的 seq，只有单聊有意义
  peerReadSeq: number
  unread: number
  // 不能发言的原因（被禁言、全员禁言、群已解散、不是好友），有值时输入框替换为说明条
  blockedReason?: string
}

// 对应 FileContent：名称、大小、类型以 media 的记录为准，客户端上送的值会被服务端覆盖
export interface FileMeta {
  fileId: string
  accessKey: string
  name: string
  size: number
  mimeType: string
}

// 对应 ImageContent：只有生成了缩略图的图片才能作为图片消息发送
export interface ImageMeta extends FileMeta {
  width: number
  height: number
}

export type MessageContent =
  | { kind: "text"; text: string }
  | { kind: "image"; image: ImageMeta }
  | { kind: "file"; file: FileMeta }

// 上传中的本地状态。上传完成拿到 file_id 和 access_key 之后，消息才进入发送队列
export interface UploadState {
  // 0～1
  progress: number
}

// uploading：文件还在上传；pending：已放入发送队列，等待 SendAck
export type SendStatus = "uploading" | "pending" | "sent" | "failed"

export interface ChatMessage {
  // 发送者 ID 与 client_msg_id 的组合（realtime/convert.ts 的 messageId）。client_msg_id 发送前生成，
  // 重试时保持不变，服务端据此去重；确认前后 ID 不变，界面不会把它当成新消息
  id: string
  conversationId: string
  senderId: string
  // 收到 SendAck 后才有
  seq?: number
  content: MessageContent
  sentAt: number
  status: SendStatus
  failReason?: string
  upload?: UploadState
  // 本地选择的图片在上传期间和上传后直接用 object URL 显示，不必再下载一遍
  localUrl?: string
}

// 单聊里标「已读」的消息（设计 3.6.3）：只标我方最新一条已确认的消息，并且要求它之后对方没有再发言
// （对方回复了自然是看过了）。发送中和失败的消息跳过，新消息确认前「已读」留在上一条上，不会闪烁。
// 已读只看单聊的 peer_read_seq，不单独存储
export function readMarkId(messages: readonly ChatMessage[], conversation: Conversation, meId: string): string | undefined {
  if (conversation.kind !== "single") return undefined
  for (let i = messages.length - 1; i >= 0; i--) {
    const m = messages[i]
    if (m.senderId !== meId) return undefined
    if (m.status !== "sent" || m.seq === undefined) continue
    return m.seq <= conversation.peerReadSeq ? m.id : undefined
  }
  return undefined
}

// 会话列表的摘要（设计 3.6.2）
export function contentPreview(content: MessageContent): string {
  switch (content.kind) {
    case "text":
      return content.text
    case "image":
      return "[图片]"
    case "file":
      return `[文件] ${content.file.name}`
  }
}
