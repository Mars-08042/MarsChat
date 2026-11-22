import { motion } from "motion/react"
import { NavLink, useMatch } from "react-router"
import { MarsLogo } from "@/components/common/MarsLogo"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { springs } from "@/lib/motion"
import { cn } from "@/lib/utils"
import { AccountMenu } from "./AccountMenu"
import { NAV_ITEMS, type NavItem } from "./nav-items"
import { useUnreadTotal } from "./use-unread-total"

// 左侧导航栏。加 .dark 类做成深色岛，亮色主题下和浅色内容区形成对比
export function NavRail({ className }: { className?: string }) {
  return (
    <nav
      aria-label="主导航"
      className={cn("dark flex w-[4.25rem] shrink-0 flex-col items-center border-r bg-background py-4 text-foreground", className)}
    >
      <MarsLogo className="mb-6 size-9" />
      <ul className="flex flex-1 flex-col items-center gap-1.5">
        {NAV_ITEMS.map((item) => (
          <li key={item.to}>
            <NavRailItem item={item} />
          </li>
        ))}
      </ul>
      <AccountMenu />
    </nav>
  )
}

function NavRailItem({ item }: { item: NavItem }) {
  const active = useMatch({ path: item.to, end: false }) !== null
  const unread = useUnreadTotal()
  const Icon = item.icon

  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <NavLink
            to={item.to}
            aria-label={item.label}
            className={cn(
              "relative flex size-11 items-center justify-center rounded-xl transition-colors outline-none focus-visible:ring-2 focus-visible:ring-ring",
              active ? "text-brand" : "text-muted-foreground hover:bg-muted hover:text-foreground",
            )}
          />
        }
      >
        {/* 选中背景用 layoutId 在各项之间滑动 */}
        {active && (
          <motion.span
            layoutId="nav-active"
            transition={springs.smooth}
            className="absolute inset-0 rounded-xl bg-brand/15 ring-1 ring-brand/25"
          />
        )}
        <Icon className="relative size-5" strokeWidth={active ? 2.25 : 2} />
        {item.showUnread && unread > 0 && (
          <span className="absolute -top-0.5 -right-0.5 min-w-4 rounded-full bg-gold px-1 text-center font-mono text-[10px] leading-4 font-semibold text-gold-foreground ring-2 ring-background">
            {unread > 99 ? "99+" : unread}
          </span>
        )}
      </TooltipTrigger>
      <TooltipContent side="right">{item.label}</TooltipContent>
    </Tooltip>
  )
}
