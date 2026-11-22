// 组件里使用下载地址（设计 3.6.9）：先同步查缓存，没有可复用的地址时挂载后换取；
// 同一轮渲染里的多个请求由 api/files.ts 合并成一次接口调用。

import { useEffect, useState } from "react"
import { downloadUrls } from "@/api/client"
import { Variant } from "@/gen/media/v1/media_pb"

export interface DownloadUrlState {
  url?: string
  // 无权访问或文件不存在，界面显示「已失效」
  missing: boolean
  // 换取地址失败（网络错误等）或重新换取后仍加载失败
  failed: boolean
  // 图片加载失败时调用：地址可能已过期或签名失效（DOWNLOAD_URL_INVALID），丢掉缓存重新换取一次
  onError: () => void
  retry: () => void
}

interface State {
  key: string
  // undefined：还没有可用的地址；null：无权访问或不存在
  url: string | null | undefined
  failed: boolean
  // 加载失败后已经重新换取过一次
  refreshed: boolean
}

const initial = (key: string, fileId: string | undefined, variant: Variant): State => ({
  key,
  url: fileId ? downloadUrls.peek(fileId, variant) : undefined,
  failed: false,
  refreshed: false,
})

// enabled 为 false 时不换取（例如大图查看器没打开时不取原图）
export function useDownloadUrl(fileId: string | undefined, variant: Variant, accessKey = "", enabled = true): DownloadUrlState {
  const key = fileId ? `${fileId}:${variant}` : ""
  const [stored, setState] = useState<State>(() => initial(key, fileId, variant))
  let state = stored
  if (stored.key !== key) {
    state = initial(key, fileId, variant)
    setState(state)
  }

  const need = Boolean(fileId) && enabled && state.url === undefined && !state.failed
  useEffect(() => {
    if (!need || !fileId) return
    let cancelled = false
    downloadUrls.get(fileId, variant, accessKey).then(
      (url) => !cancelled && setState((s) => (s.key === key ? { ...s, url } : s)),
      () => !cancelled && setState((s) => (s.key === key ? { ...s, failed: true } : s)),
    )
    return () => {
      cancelled = true
    }
  }, [need, fileId, variant, accessKey, key])

  return {
    url: state.url ?? undefined,
    missing: state.url === null,
    failed: state.failed,
    onError: () => {
      if (!fileId) return
      if (state.refreshed) {
        setState((s) => (s.key === key ? { ...s, failed: true } : s))
        return
      }
      downloadUrls.invalidate(fileId, variant)
      setState((s) => (s.key === key ? { ...s, url: undefined, refreshed: true } : s))
    },
    retry: () => {
      if (fileId) downloadUrls.invalidate(fileId, variant)
      setState((s) => (s.key === key ? { ...s, url: undefined, failed: false, refreshed: false } : s))
    },
  }
}

// 头像：优先取缩略图（最长边 320 像素）；超过像素上限的图片没有缩略图，再取原图（头像不超过 5 MiB）。
// 头像文件所有登录用户都能下载，不需要访问密钥
export function useAvatarUrl(avatarId: string | undefined): { url?: string; onError: () => void } {
  const id = avatarId || undefined
  const thumb = useDownloadUrl(id, Variant.THUMBNAIL)
  const original = useDownloadUrl(id, Variant.ORIGINAL, "", thumb.missing)
  return thumb.missing ? { url: original.url, onError: original.onError } : { url: thumb.url, onError: thumb.onError }
}
