// 文件与图片消息的前端规则（设计 3.6.9）。上限与 media 服务的 policy 配置一致（media-service.md 3.3），
// 前端检查只是为了更早提示，最终以服务端校验为准。

const MiB = 1024 * 1024

export const MAX_CHAT_FILE_BYTES = 100 * MiB
// 超过这个大小的图片服务端不生成缩略图，只能按文件发送
export const MAX_THUMBNAIL_SOURCE_BYTES = 10 * MiB
// 超过这个像素数的图片同样不生成缩略图
export const MAX_THUMBNAIL_SOURCE_PIXELS = 16_000_000
export const MAX_AVATAR_BYTES = 5 * MiB

// 服务端按文件内容识别为图片候选的类型；扩展名和浏览器给出的类型只用于前端预判
const IMAGE_TYPES = new Set(["image/jpeg", "image/png", "image/gif", "image/webp"])

export function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  const units = ["KB", "MB", "GB"]
  let value = bytes / 1024
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit++
  }
  // 小于 10 保留一位小数，否则取整，避免「100.0 MB」
  return `${value < 10 ? value.toFixed(1) : Math.round(value)} ${units[unit]}`
}

export type UploadPlan = { ok: true; as: "image" | "file" } | { ok: false; reason: string }

// 决定一个本地文件按图片还是文件发送。像素数要解码后才知道，由调用方在拿到宽高后传入
export function planChatUpload(file: { size: number; type: string }, pixels?: number): UploadPlan {
  if (file.size === 0) return { ok: false, reason: "不能发送空文件" }
  if (file.size > MAX_CHAT_FILE_BYTES) return { ok: false, reason: `文件超过 ${formatBytes(MAX_CHAT_FILE_BYTES)} 上限` }
  const thumbnailable =
    IMAGE_TYPES.has(file.type) &&
    file.size <= MAX_THUMBNAIL_SOURCE_BYTES &&
    (pixels === undefined || pixels <= MAX_THUMBNAIL_SOURCE_PIXELS)
  return { ok: true, as: thumbnailable ? "image" : "file" }
}

export function checkAvatar(file: { size: number; type: string }): string | null {
  if (!IMAGE_TYPES.has(file.type)) return "头像只支持 JPEG、PNG、GIF、WebP 图片"
  if (file.size > MAX_AVATAR_BYTES) return `头像不能超过 ${formatBytes(MAX_AVATAR_BYTES)}`
  return null
}

export type FileCategory = "image" | "video" | "audio" | "archive" | "pdf" | "doc" | "sheet" | "slide" | "code" | "text" | "other"

const EXT_CATEGORY: Record<string, FileCategory> = {
  zip: "archive", rar: "archive", "7z": "archive", tar: "archive", gz: "archive", tgz: "archive",
  pdf: "pdf",
  doc: "doc", docx: "doc", pages: "doc", rtf: "doc",
  xls: "sheet", xlsx: "sheet", csv: "sheet", numbers: "sheet",
  ppt: "slide", pptx: "slide", key: "slide",
  ts: "code", tsx: "code", js: "code", go: "code", py: "code", java: "code", json: "code", yaml: "code", yml: "code", proto: "code", sql: "code", sh: "code",
  txt: "text", md: "text", log: "text",
}

// 用于选择文件卡片的图标。服务端返回的 mime_type 对非图片文件可能只是 application/octet-stream，所以优先看扩展名
export function fileCategory(name: string, mimeType: string): FileCategory {
  const ext = splitFileName(name)[1].toLowerCase()
  if (ext && EXT_CATEGORY[ext]) return EXT_CATEGORY[ext]
  if (mimeType.startsWith("image/")) return "image"
  if (mimeType.startsWith("video/")) return "video"
  if (mimeType.startsWith("audio/")) return "audio"
  if (mimeType.startsWith("text/")) return "text"
  return "other"
}

// 拆成主名和扩展名，界面上截断主名、保留扩展名。点开头的隐藏文件（.env）没有扩展名
export function splitFileName(name: string): [base: string, ext: string] {
  const dot = name.lastIndexOf(".")
  if (dot <= 0 || dot === name.length - 1) return [name, ""]
  return [name.slice(0, dot), name.slice(dot + 1)]
}

// 图片消息的显示尺寸：按原图宽高等比缩放，宽不超过 maxWidth、高不超过 maxHeight；
// 极窄或极扁的图片设最小边，超出部分由 object-cover 裁掉，避免出现一条细线
export function fitImage(
  width: number,
  height: number,
  { maxWidth = 280, maxHeight = 320, minSide = 96 } = {},
): { width: number; height: number } {
  if (width <= 0 || height <= 0) return { width: maxWidth, height: Math.round(maxWidth * 0.75) }
  const scale = Math.min(1, maxWidth / width, maxHeight / height)
  return {
    width: Math.round(Math.min(maxWidth, Math.max(minSide, width * scale))),
    height: Math.round(Math.min(maxHeight, Math.max(minSide, height * scale))),
  }
}
