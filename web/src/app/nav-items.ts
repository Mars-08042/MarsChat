import { MessageCircle, Settings, Users, UsersRound, type LucideIcon } from "lucide-react"

export interface NavItem {
  to: string
  label: string
  icon: LucideIcon
  // 角标：unread 为会话未读总数，requests 为待处理的好友申请数
  badge?: "unread" | "requests"
}

export const NAV_ITEMS: readonly NavItem[] = [
  { to: "/chat", label: "消息", icon: MessageCircle, badge: "unread" },
  { to: "/contacts", label: "联系人", icon: Users, badge: "requests" },
  { to: "/groups", label: "群组", icon: UsersRound },
  { to: "/settings", label: "设置", icon: Settings },
]
