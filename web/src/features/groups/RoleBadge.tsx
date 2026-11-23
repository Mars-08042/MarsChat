import { cn } from "@/lib/utils"
import { ROLE_LABEL } from "./model"

// 群主用沙金底、管理员用浅红底的小徽章（设计 3.6.7）
export function RoleBadge({ role, className }: { role: "owner" | "admin"; className?: string }) {
  return (
    <span
      className={cn(
        "shrink-0 rounded px-1.5 py-px text-[11px] leading-4 font-medium",
        role === "owner" ? "bg-gold/20 text-foreground" : "bg-brand/10 text-brand-text",
        className,
      )}
    >
      {ROLE_LABEL[role]}
    </span>
  )
}
