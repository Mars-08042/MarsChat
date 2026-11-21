import { MessageCircle, Settings, Users, UsersRound, type LucideIcon } from "lucide-react"

export interface NavItem {
  to: string
  label: string
  icon: LucideIcon
  // 角标显示会话未读总数
  showUnread?: boolean
}

export const NAV_ITEMS: readonly NavItem[] = [
  { to: "/chat", label: "消息", icon: MessageCircle, showUnread: true },
  { to: "/contacts", label: "联系人", icon: Users },
  { to: "/groups", label: "群组", icon: UsersRound },
  { to: "/settings", label: "设置", icon: Settings },
]
