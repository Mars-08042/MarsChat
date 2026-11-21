import { useEffect } from "react"
import { useChatStore } from "@/features/chat/fake-store"

// 所有会话的未读总数。选择器返回数字，store 其他字段变化时不会触发重渲染
export function useUnreadTotal(): number {
  return useChatStore((s) => Object.values(s.conversations).reduce((sum, c) => sum + c.unread, 0))
}

// 浏览器标签页标题显示未读总数，例如「(3) MarsChat」（设计 3.6.2）
export function useUnreadTitle(): void {
  const total = useUnreadTotal()
  useEffect(() => {
    document.title = total > 0 ? `(${total > 99 ? "99+" : total}) MarsChat` : "MarsChat"
    return () => {
      document.title = "MarsChat"
    }
  }, [total])
}
