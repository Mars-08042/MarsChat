import { describe, expect, it } from "vitest"
import {
  checkAvatar,
  fileCategory,
  fitImage,
  formatBytes,
  MAX_AVATAR_BYTES,
  MAX_CHAT_FILE_BYTES,
  MAX_THUMBNAIL_SOURCE_BYTES,
  MAX_THUMBNAIL_SOURCE_PIXELS,
  planChatUpload,
  splitFileName,
} from "./files"

const MiB = 1024 * 1024

describe("formatBytes", () => {
  it.each([
    [0, "0 B"],
    [1023, "1023 B"],
    [1024, "1.0 KB"],
    [1536, "1.5 KB"],
    [20 * 1024, "20 KB"],
    [3.4 * MiB, "3.4 MB"],
    [100 * MiB, "100 MB"],
    [1024 * MiB, "1.0 GB"],
  ])("%d → %s", (bytes, want) => {
    expect(formatBytes(bytes)).toBe(want)
  })
})

describe("planChatUpload", () => {
  it("可生成缩略图的图片按图片发送", () => {
    expect(planChatUpload({ size: 2 * MiB, type: "image/png" })).toEqual({ ok: true, as: "image" })
    expect(planChatUpload({ size: MAX_THUMBNAIL_SOURCE_BYTES, type: "image/webp" })).toEqual({ ok: true, as: "image" })
  })

  it("超过 10 MiB 的图片按文件发送", () => {
    expect(planChatUpload({ size: MAX_THUMBNAIL_SOURCE_BYTES + 1, type: "image/jpeg" })).toEqual({ ok: true, as: "file" })
  })

  it("超过 1600 万像素的图片按文件发送", () => {
    expect(planChatUpload({ size: MiB, type: "image/jpeg" }, MAX_THUMBNAIL_SOURCE_PIXELS + 1)).toEqual({ ok: true, as: "file" })
    expect(planChatUpload({ size: MiB, type: "image/jpeg" }, MAX_THUMBNAIL_SOURCE_PIXELS)).toEqual({ ok: true, as: "image" })
  })

  it("服务端不生成缩略图的图片类型按文件发送", () => {
    expect(planChatUpload({ size: 1024, type: "image/svg+xml" })).toEqual({ ok: true, as: "file" })
    expect(planChatUpload({ size: 1024, type: "image/heic" })).toEqual({ ok: true, as: "file" })
  })

  it("100 MiB 恰好可以发送，超过不行", () => {
    expect(planChatUpload({ size: MAX_CHAT_FILE_BYTES, type: "application/zip" })).toEqual({ ok: true, as: "file" })
    expect(planChatUpload({ size: MAX_CHAT_FILE_BYTES + 1, type: "application/zip" })).toMatchObject({ ok: false })
  })

  it("空文件不能发送", () => {
    expect(planChatUpload({ size: 0, type: "text/plain" })).toMatchObject({ ok: false })
  })
})

describe("checkAvatar", () => {
  it("只接受 5 MiB 以内的光栅图片", () => {
    expect(checkAvatar({ size: MAX_AVATAR_BYTES, type: "image/png" })).toBeNull()
    expect(checkAvatar({ size: MAX_AVATAR_BYTES + 1, type: "image/png" })).not.toBeNull()
    expect(checkAvatar({ size: 1024, type: "image/svg+xml" })).not.toBeNull()
    expect(checkAvatar({ size: 1024, type: "application/pdf" })).not.toBeNull()
  })
})

describe("splitFileName", () => {
  it.each([
    ["report.pdf", ["report", "pdf"]],
    ["archive.tar.gz", ["archive.tar", "gz"]],
    [".env", [".env", ""]],
    ["README", ["README", ""]],
    ["trailing.", ["trailing.", ""]],
  ])("%s", (name, want) => {
    expect(splitFileName(name)).toEqual(want)
  })
})

describe("fileCategory", () => {
  it("优先按扩展名判断", () => {
    expect(fileCategory("设计稿.PDF", "application/octet-stream")).toBe("pdf")
    expect(fileCategory("frame.proto", "application/octet-stream")).toBe("code")
    expect(fileCategory("backup.7z", "application/octet-stream")).toBe("archive")
  })

  it("没有已知扩展名时按类型判断", () => {
    expect(fileCategory("clip", "video/mp4")).toBe("video")
    expect(fileCategory("unknown.bin", "application/octet-stream")).toBe("other")
  })
})

describe("fitImage", () => {
  it("小图保持原尺寸", () => {
    expect(fitImage(200, 150)).toEqual({ width: 200, height: 150 })
  })

  it("横图按宽度缩放", () => {
    expect(fitImage(1920, 1080)).toEqual({ width: 280, height: 158 })
  })

  it("竖图按高度缩放", () => {
    expect(fitImage(1080, 1440)).toEqual({ width: 240, height: 320 })
  })

  it("极扁的图片有最小高度", () => {
    expect(fitImage(4000, 100)).toEqual({ width: 280, height: 96 })
  })

  it("宽高未知时给出默认占位", () => {
    expect(fitImage(0, 0)).toEqual({ width: 280, height: 210 })
  })
})
