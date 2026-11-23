// 下载聊天里的文件或原图（设计 3.6.9）：先换取原文件的下载地址，再用临时链接触发下载。
// 缓存只复用剩余时间超过 1 分钟的地址，点击后立即使用，不会用到过期的地址。

import { downloadUrls } from "@/api/client"
import { Variant } from "@/gen/media/v1/media_pb"
import type { FileMeta } from "./types"

// 返回 false 表示文件无权访问或不存在
export async function downloadFile(file: Pick<FileMeta, "fileId" | "accessKey" | "name">): Promise<boolean> {
  const url = await downloadUrls.get(file.fileId, Variant.ORIGINAL, file.accessKey)
  if (!url) return false
  saveAs(url, file.name)
  return true
}

// 非图片文件服务端以 attachment 返回，浏览器只会保存；图片是 inline，同源时 download 属性让它保存而不是打开
export function saveAs(url: string, name: string) {
  const a = document.createElement("a")
  a.href = url
  a.download = name
  a.rel = "noopener"
  document.body.appendChild(a)
  a.click()
  a.remove()
}
