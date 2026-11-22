// 聊天文件的上传（设计 3.6.9、3.9.4）：选中文件后立即在发件箱插入「上传中」的消息，
// 上传完成拿到 file_id 和 access_key 后交给发送队列，之后和文字消息一样等待确认。
// - 取消：中止请求，消息直接移除，释放本地预览地址；
// - 上传失败：消息标为失败，重试时重新上传（保留 File 对象）；发送阶段的失败由发送队列重试；
// - 服务端返回的不是带缩略图的图片时改按文件消息发送，避免 message 返回 FILE_INVALID。
// 只存在内存里：刷新页面会丢掉上传中的消息，离开页面前由 beforeunload 提示。

import { uploadErrorText } from "@/api/errors"
import { isUploadAborted, type UploadOptions, type UploadTask } from "@/api/files"
import type { ChatMessage, FileMeta, MessageContent } from "@/features/chat/types"
import { FileKind, Purpose, type FileInfo } from "@/gen/media/v1/media_pb"
import type { ConversationType } from "@/gen/message/v1/message_pb"
import type { Outgoing } from "./send-queue"

export interface UploadDeps {
  upload: (file: Blob, options: UploadOptions) => UploadTask
  // 插入或更新发件箱里的消息（上传中、进度、失败）
  onUpdate: (message: ChatMessage) => void
  // 取消上传或客户端停止时从发件箱移除
  onRemove: (messageId: string) => void
  // 上传完成，交给发送队列
  onUploaded: (message: ChatMessage, outgoing: Outgoing) => void
  revokeUrl?: (url: string) => void
}

// OutgoingMessage 里除内容以外的字段：接收方和 client_msg_id
export interface OutgoingTarget {
  peerType: ConversationType
  peerId: bigint
  clientMsgId: string
}

export interface UploadRequest {
  // 状态为 uploading 的消息，内容里是本地的文件名、大小、类型（图片带宽高），图片带本地预览地址
  message: ChatMessage
  file: File
  as: "image" | "file"
  outgoing: OutgoingTarget
}

export interface UploadManager {
  start(request: UploadRequest): void
  cancel(messageId: string): void
  // 上传阶段失败的消息重新上传；返回 false 表示不是这里的消息（发送阶段的失败交给发送队列）
  retry(messageId: string): boolean
  // 还有正在上传的文件，离开页面前要提示
  hasActive(): boolean
  // 客户端停止（退出、被踢）：中止全部上传，移除这些消息，释放本地预览地址
  clear(): void
}

interface Job {
  request: UploadRequest
  message: ChatMessage
  task?: UploadTask
  state: "uploading" | "failed"
}

// 上传返回的文件信息转成消息内容。只有服务端生成了缩略图的图片才按图片发送
export function contentFromFile(info: FileInfo, as: "image" | "file"): { content: MessageContent; outgoing: Outgoing["content"] } {
  const meta: FileMeta = {
    fileId: info.id.toString(),
    accessKey: info.accessKey,
    name: info.name,
    size: Number(info.size),
    mimeType: info.mimeType,
  }
  const base = { fileId: info.id, accessKey: info.accessKey, name: info.name, size: info.size, mimeType: info.mimeType }
  if (as === "image" && info.kind === FileKind.IMAGE && info.hasThumbnail) {
    return {
      content: { kind: "image", image: { ...meta, width: info.width, height: info.height } },
      outgoing: { body: { case: "image", value: { ...base, width: info.width, height: info.height } } },
    }
  }
  return { content: { kind: "file", file: meta }, outgoing: { body: { case: "file", value: base } } }
}

export function createUploadManager(deps: UploadDeps): UploadManager {
  const revoke = deps.revokeUrl ?? ((url: string) => URL.revokeObjectURL(url))
  const jobs = new Map<string, Job>()
  // 已发出的图片继续用本地地址显示（不再下载一遍），客户端停止时统一释放
  const keptUrls = new Set<string>()

  const update = (job: Job, patch: Partial<ChatMessage>) => {
    job.message = { ...job.message, ...patch }
    deps.onUpdate(job.message)
  }

  const run = (job: Job) => {
    const id = job.message.id
    job.state = "uploading"
    let shown = -1
    update(job, { status: "uploading", upload: { progress: 0 }, failReason: undefined })
    const task = deps.upload(job.request.file, {
      name: job.request.file.name,
      purpose: Purpose.CHAT,
      onProgress: (loaded, total) => {
        // 进度按百分比取整后有变化才写回，避免每个 progress 事件都触发渲染
        const percent = total > 0 ? Math.floor((loaded / total) * 100) : 0
        if (percent === shown || jobs.get(id) !== job) return
        shown = percent
        update(job, { upload: { progress: percent / 100 } })
      },
    })
    job.task = task
    task.promise.then(
      (info) => {
        if (jobs.get(id) !== job) return
        jobs.delete(id)
        const { content, outgoing } = contentFromFile(info, job.request.as)
        let localUrl = job.message.localUrl
        if (localUrl && content.kind !== "image") {
          revoke(localUrl)
          localUrl = undefined
        }
        if (localUrl) keptUrls.add(localUrl)
        const message: ChatMessage = { ...job.message, content, localUrl, upload: undefined, status: "pending" }
        deps.onUploaded(message, { ...job.request.outgoing, content: outgoing })
      },
      (e: unknown) => {
        if (jobs.get(id) !== job || isUploadAborted(e)) return
        job.state = "failed"
        update(job, { status: "failed", upload: undefined, failReason: uploadErrorText(e) })
      },
    )
  }

  const drop = (job: Job) => {
    jobs.delete(job.message.id)
    job.task?.abort()
    if (job.message.localUrl) revoke(job.message.localUrl)
    deps.onRemove(job.message.id)
  }

  return {
    start(request) {
      const job: Job = { request, message: request.message, state: "uploading" }
      jobs.set(request.message.id, job)
      run(job)
    },
    cancel(messageId) {
      const job = jobs.get(messageId)
      if (job) drop(job)
    },
    retry(messageId) {
      const job = jobs.get(messageId)
      if (!job) return false
      if (job.state === "failed") run(job)
      return true
    },
    hasActive: () => [...jobs.values()].some((j) => j.state === "uploading"),
    clear() {
      for (const job of [...jobs.values()]) drop(job)
      keptUrls.forEach((url) => revoke(url))
      keptUrls.clear()
    },
  }
}
