import { useEffect } from "react"
import { useReceivedRequests } from "@/features/contacts/queries"
import { useChat } from "@/stores/chat"
import type { NavItem } from "./nav-items"

// 所有会话的未读总数。选择器返回数字，store 其他字段变化时不会触发重渲染
export function useUnreadTotal(): number {
  return useChat((s) => Object.values(s.conversations).reduce((sum, c) => sum + c.unread, 0))
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

// 导航项的角标数字；没有角标的项返回 0
export function useNavBadge(badge: NavItem["badge"]): number {
  const unread = useUnreadTotal()
  // 收到的申请只返回待处理的，数量就是角标
  const requests = useReceivedRequests().data?.length ?? 0
  if (badge === "unread") return unread
  if (badge === "requests") return requests
  return 0
}
