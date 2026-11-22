import { motion } from "motion/react"
import { NavLink } from "react-router"
import { springs } from "@/lib/motion"
import { cn } from "@/lib/utils"

export interface PageTab {
  to: string
  label: string
  // 大于 0 时在标签旁显示金色计数
  badge?: number
}

// 页面内的分页标签，每个标签对应一个路由（联系人、设置）。选中的底块在标签间滑动
export function PageTabs({ label, tabs, active, layoutId }: { label: string; tabs: readonly PageTab[]; active: string; layoutId: string }) {
  return (
    <nav aria-label={label} className="flex w-fit max-w-full gap-1 overflow-x-auto rounded-xl bg-muted p-1 [scrollbar-width:none]">
      {tabs.map((t) => (
        <NavLink
          key={t.to}
          to={t.to}
          end
          className={cn(
            "relative shrink-0 rounded-lg px-4 py-1.5 text-sm font-medium transition-colors outline-none focus-visible:ring-2 focus-visible:ring-ring",
            active === t.to ? "text-foreground" : "text-muted-foreground hover:text-foreground",
          )}
        >
          {active === t.to && <motion.span layoutId={layoutId} transition={springs.smooth} className="absolute inset-0 rounded-lg bg-background shadow-sm" />}
          <span className="relative inline-flex items-center gap-1.5">
            {t.label}
            {t.badge !== undefined && t.badge > 0 && (
              <span className="min-w-4 rounded-full bg-gold px-1 text-center font-mono text-[10px] leading-4 font-semibold text-gold-foreground">
                {t.badge > 99 ? "99+" : t.badge}
              </span>
            )}
          </span>
        </NavLink>
      ))}
    </nav>
  )
}
