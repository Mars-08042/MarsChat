// 聊天界面使用的数据结构。字段对应 message 服务的会话与消息、media 服务的文件（设计第 4 节），
// 原型阶段由假数据填充，第 4 阶段改由同步引擎写入。

export interface ChatUser {
  id: string
  nickname: string
  // media 的头像文件 ID 换到的地址；原型阶段直接是图片地址
  avatarUrl?: string
}

export interface Conversation {
  id: string
  kind: "single" | "group"
  title: string
  avatarUrl?: string
  // 单聊对方的用户 ID
  peerId?: string
  // 群成员，原型阶段用来挑选模拟消息的发送者
  memberIds?: string[]
  memberCount?: number
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
  // client_msg_id：发送前生成，重试时保持不变，服务端据此去重
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

export type DisplayStatus = SendStatus | "read"

// 已读只看单聊的 peer_read_seq（设计 3.6.3），不单独存储
export function displayStatus(message: ChatMessage, conversation: Conversation): DisplayStatus {
  if (message.status !== "sent") return message.status
  const read = conversation.kind === "single" && message.seq !== undefined && message.seq <= conversation.peerReadSeq
  return read ? "read" : "sent"
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
