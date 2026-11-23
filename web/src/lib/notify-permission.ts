import { useCallback, useEffect, useState } from "react"

// 系统通知的权限状态（设置页的通知页签，来电通知见 features/call/alerts.ts）。
// 浏览器只在安全上下文（HTTPS 或 localhost）提供通知；不支持和非安全上下文分开提示
export type NotifyPermission = "unsupported" | "insecure" | NotificationPermission

export function readNotifyPermission(): NotifyPermission {
  if (!window.isSecureContext) return "insecure"
  if (typeof Notification === "undefined") return "unsupported"
  return Notification.permission
}

export function useNotifyPermission(): { permission: NotifyPermission; request: () => Promise<void> } {
  const [permission, setPermission] = useState(readNotifyPermission)

  useEffect(() => {
    // 用户可能在浏览器的网站设置里改了权限，切回页面时重新读取
    const update = () => setPermission(readNotifyPermission())
    window.addEventListener("focus", update)
    document.addEventListener("visibilitychange", update)
    return () => {
      window.removeEventListener("focus", update)
      document.removeEventListener("visibilitychange", update)
    }
  }, [])

  // 必须在用户点击时调用，浏览器才会弹出询问；已经拒绝过时浏览器不再询问，直接返回 denied
  const request = useCallback(async () => {
    if (readNotifyPermission() !== "default") return
    await Notification.requestPermission()
    setPermission(readNotifyPermission())
  }, [])

  return { permission, request }
}
