import { MessageCircle, Settings, ShieldCheck, Users, UsersRound, type LucideIcon } from "lucide-react"
import { Role } from "@/gen/account/v1/account_pb"
import { useSession } from "@/stores/session"

export interface NavItem {
  to: string
  label: string
  icon: LucideIcon
  // 角标：unread 为会话未读总数，requests 为待处理的好友申请数
  badge?: "unread" | "requests"
  // 只对系统管理员显示（设计 3.5 的 /admin）
  adminOnly?: boolean
}

export const NAV_ITEMS: readonly NavItem[] = [
  { to: "/chat", label: "消息", icon: MessageCircle, badge: "unread" },
  { to: "/contacts", label: "联系人", icon: Users, badge: "requests" },
  { to: "/groups", label: "群组", icon: UsersRound },
  { to: "/admin", label: "管理", icon: ShieldCheck, adminOnly: true },
  { to: "/settings", label: "设置", icon: Settings },
]

// 当前用户可见的导航项。角色取 GetMe 的资料，资料加载前不显示管理入口
export function useNavItems(): readonly NavItem[] {
  const admin = useSession((s) => s.user?.role === Role.ADMIN)
  return admin ? NAV_ITEMS : NAV_ITEMS.filter((item) => !item.adminOnly)
}
