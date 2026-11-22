import { Toaster as Sonner } from "sonner"
import { DESKTOP_QUERY, useMediaQuery } from "@/lib/use-media-query"

// 全局轻提示（sonner）。关闭自带样式，用设计变量画成浮层卡片，深浅色主题跟随 CSS 变量自动切换。
// 桌面端在右下角，不挡输入框和会话列表；手机端在顶部，避开底部导航栏
export function Toaster() {
  const desktop = useMediaQuery(DESKTOP_QUERY)
  return (
    <Sonner
      position={desktop ? "bottom-right" : "top-center"}
      visibleToasts={4}
      toastOptions={{
        unstyled: true,
        classNames: {
          toast:
            "flex w-[var(--width)] items-center gap-3 rounded-card border bg-popover px-4 py-3 text-sm text-popover-foreground shadow-float",
          content: "flex min-w-0 flex-1 flex-col gap-0.5",
          title: "font-medium",
          description: "text-xs text-muted-foreground",
          icon: "shrink-0",
          actionButton:
            "shrink-0 rounded-lg bg-brand-strong px-2.5 py-1 text-xs font-medium text-white transition-colors hover:bg-[color-mix(in_oklch,var(--brand-strong),black_14%)]",
          cancelButton: "shrink-0 rounded-lg px-2.5 py-1 text-xs font-medium text-muted-foreground hover:bg-muted",
        },
      }}
    />
  )
}
