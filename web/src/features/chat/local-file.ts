// 把用户选择、拖入或粘贴的 File 转成待上传的描述：图片先在本地解码拿到宽高，
// 既用来判断能否按图片发送（像素上限），也用来在上传期间按比例占位。

import type { LocalFile } from "./fake-store"

function readImageSize(url: string): Promise<{ width: number; height: number }> {
  return new Promise((resolve, reject) => {
    const img = new Image()
    img.onload = () => resolve({ width: img.naturalWidth, height: img.naturalHeight })
    img.onerror = () => reject(new Error("image decode failed"))
    img.src = url
  })
}

export async function toLocalFile(file: File): Promise<LocalFile> {
  const base = { name: file.name, size: file.size, type: file.type }
  if (!file.type.startsWith("image/")) return base
  const url = URL.createObjectURL(file)
  try {
    return { ...base, url, ...(await readImageSize(url)) }
  } catch {
    // 浏览器解码不了（例如 HEIC），按普通文件发送
    URL.revokeObjectURL(url)
    return base
  }
}
