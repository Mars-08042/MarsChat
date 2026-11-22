import { describe, expect, it, vi } from "vitest"
import { create } from "@bufbuild/protobuf"
import { ApiError, NETWORK } from "@/api/errors"
import { UPLOAD_ABORTED, type UploadOptions } from "@/api/files"
import type { ChatMessage } from "@/features/chat/types"
import type { MessageInitShape } from "@bufbuild/protobuf"
import { FileInfoSchema, FileKind, Purpose, type FileInfo } from "@/gen/media/v1/media_pb"
import { ConversationType } from "@/gen/message/v1/message_pb"
import { contentFromFile, createUploadManager } from "./uploads"

interface FakeTask {
  options: UploadOptions
  resolve: (info: FileInfo) => void
  reject: (e: unknown) => void
  aborted: boolean
}

function setup() {
  const tasks: FakeTask[] = []
  const updates: ChatMessage[] = []
  const deps = {
    upload: vi.fn((_file: Blob, options: UploadOptions) => {
      let resolve!: (info: FileInfo) => void
      let reject!: (e: unknown) => void
      const promise = new Promise<FileInfo>((res, rej) => {
        resolve = res
        reject = rej
      })
      const task: FakeTask = { options, resolve, reject, aborted: false }
      tasks.push(task)
      return {
        promise,
        abort: () => {
          task.aborted = true
          reject(new ApiError(0, UPLOAD_ABORTED, "aborted"))
        },
      }
    }),
    onUpdate: vi.fn((m: ChatMessage) => updates.push(m)),
    onRemove: vi.fn(),
    onUploaded: vi.fn(),
    revokeUrl: vi.fn(),
  }
  const manager = createUploadManager(deps)
  return { manager, deps, tasks, updates, last: () => updates[updates.length - 1] }
}

const outgoing = { peerType: ConversationType.GROUP, peerId: 7n, clientMsgId: "c1" }

const imageMessage = (): ChatMessage => ({
  id: "1:c1",
  conversationId: "7",
  senderId: "1",
  content: { kind: "image", image: { fileId: "", accessKey: "", name: "a.png", size: 2048, mimeType: "image/png", width: 800, height: 600 } },
  sentAt: 0,
  status: "uploading",
  upload: { progress: 0 },
  localUrl: "blob:a",
})

const info = (patch: MessageInitShape<typeof FileInfoSchema> = {}) =>
  create(FileInfoSchema, {
    id: 42n,
    accessKey: "key",
    purpose: Purpose.CHAT,
    kind: FileKind.IMAGE,
    name: "a.png",
    size: 2000n,
    mimeType: "image/png",
    width: 800,
    height: 600,
    hasThumbnail: true,
    ...patch,
  })

const settle = () => new Promise((r) => setTimeout(r, 0))
const file = new File(["x"], "a.png", { type: "image/png" })

describe("createUploadManager", () => {
  it("上传中写入进度，百分比不变时不重复写；完成后按服务端的文件信息交给发送队列", async () => {
    const { manager, deps, tasks, updates, last } = setup()
    manager.start({ message: imageMessage(), file, as: "image", outgoing })
    expect(tasks[0].options.purpose).toBe(Purpose.CHAT)
    expect(last()).toMatchObject({ status: "uploading", upload: { progress: 0 } })
    tasks[0].options.onProgress?.(500, 1000)
    tasks[0].options.onProgress?.(501, 1000)
    expect(updates.filter((m) => m.upload?.progress === 0.5)).toHaveLength(1)
    expect(manager.hasActive()).toBe(true)

    tasks[0].resolve(info())
    await settle()
    const [message, out] = deps.onUploaded.mock.calls[0]
    expect(message).toMatchObject({ id: "1:c1", status: "pending", localUrl: "blob:a", upload: undefined })
    expect(message.content).toEqual({ kind: "image", image: { fileId: "42", accessKey: "key", name: "a.png", size: 2000, mimeType: "image/png", width: 800, height: 600 } })
    expect(out).toMatchObject({ peerType: ConversationType.GROUP, peerId: 7n, clientMsgId: "c1", content: { body: { case: "image" } } })
    expect(manager.hasActive()).toBe(false)
    // 本地地址继续用于显示，客户端停止时才释放
    expect(deps.revokeUrl).not.toHaveBeenCalled()
    manager.clear()
    expect(deps.revokeUrl).toHaveBeenCalledWith("blob:a")
  })

  it("服务端没有生成缩略图时改按文件消息发送，释放本地预览", async () => {
    const { manager, deps, tasks } = setup()
    manager.start({ message: imageMessage(), file, as: "image", outgoing })
    tasks[0].resolve(info({ hasThumbnail: false, width: 0, height: 0 }))
    await settle()
    const [message, out] = deps.onUploaded.mock.calls[0]
    expect(message.content.kind).toBe("file")
    expect(message.localUrl).toBeUndefined()
    expect(out.content.body.case).toBe("file")
    expect(deps.revokeUrl).toHaveBeenCalledWith("blob:a")
  })

  it("上传失败标为失败并带原因，重试时重新上传", async () => {
    const { manager, deps, tasks, last } = setup()
    manager.start({ message: imageMessage(), file, as: "image", outgoing })
    tasks[0].reject(new ApiError(0, NETWORK, "down"))
    await settle()
    expect(last()).toMatchObject({ status: "failed", failReason: "网络中断，文件没有传完" })
    expect(manager.hasActive()).toBe(false)

    expect(manager.retry("1:c1")).toBe(true)
    expect(deps.upload).toHaveBeenCalledTimes(2)
    expect(last()).toMatchObject({ status: "uploading", failReason: undefined })
    tasks[1].resolve(info())
    await settle()
    expect(deps.onUploaded).toHaveBeenCalledTimes(1)
    // 已经交给发送队列的消息，重试由发送队列处理
    expect(manager.retry("1:c1")).toBe(false)
  })

  it("取消时中止上传、移除消息、释放本地预览，不标为失败", async () => {
    const { manager, deps, tasks } = setup()
    manager.start({ message: imageMessage(), file, as: "image", outgoing })
    manager.cancel("1:c1")
    await settle()
    expect(tasks[0].aborted).toBe(true)
    expect(deps.onRemove).toHaveBeenCalledWith("1:c1")
    expect(deps.revokeUrl).toHaveBeenCalledWith("blob:a")
    expect(deps.onUpdate.mock.calls.some(([m]) => m.status === "failed")).toBe(false)
    expect(deps.onUploaded).not.toHaveBeenCalled()
  })

  it("客户端停止时中止全部上传并移除这些消息", async () => {
    const { manager, deps, tasks } = setup()
    manager.start({ message: imageMessage(), file, as: "image", outgoing })
    manager.start({ message: { ...imageMessage(), id: "1:c2", localUrl: undefined }, file, as: "file", outgoing: { ...outgoing, clientMsgId: "c2" } })
    manager.clear()
    await settle()
    expect(tasks.every((t) => t.aborted)).toBe(true)
    expect(deps.onRemove.mock.calls.map(([id]) => id)).toEqual(["1:c1", "1:c2"])
    expect(manager.hasActive()).toBe(false)
  })
})

describe("contentFromFile", () => {
  it("按文件发送时不带宽高", () => {
    const { content, outgoing: out } = contentFromFile(info({ kind: FileKind.FILE, hasThumbnail: false, name: "a.zip", mimeType: "application/zip" }), "file")
    expect(content).toEqual({ kind: "file", file: { fileId: "42", accessKey: "key", name: "a.zip", size: 2000, mimeType: "application/zip" } })
    expect(out?.body?.case).toBe("file")
  })

  it("计划按文件发送的图片即使有缩略图也按文件发送", () => {
    expect(contentFromFile(info(), "file").content.kind).toBe("file")
  })
})
