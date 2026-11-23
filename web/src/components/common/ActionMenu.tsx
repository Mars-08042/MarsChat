import { Menu } from "@base-ui/react/menu"
import { Ellipsis, type LucideIcon } from "lucide-react"
import { buttonVariants } from "@/components/ui/button"
import { cn } from "@/lib/utils"

export interface ActionItem {
  label: string
  icon?: LucideIcon
  onSelect: () => void
  destructive?: boolean
  disabled?: boolean
}

const ITEM =
  "flex cursor-default items-center gap-2 rounded-md px-2 py-1.5 text-sm outline-none select-none data-disabled:opacity-50 data-highlighted:bg-accent data-highlighted:text-accent-foreground [&_svg]:size-4"

// 列表行右侧的「更多」菜单：删除好友、拉黑、成员管理等不常用又需要确认的操作
export function ActionMenu({ label, items }: { label: string; items: ActionItem[] }) {
  if (items.length === 0) return null
  return (
    <Menu.Root>
      <Menu.Trigger aria-label={label} className={cn(buttonVariants({ variant: "ghost", size: "icon-sm" }), "text-muted-foreground")}>
        <Ellipsis />
      </Menu.Trigger>
      <Menu.Portal>
        <Menu.Positioner side="bottom" align="end" sideOffset={6} className="z-50 outline-none">
          <Menu.Popup className="min-w-40 origin-(--transform-origin) rounded-lg border bg-popover p-1 text-popover-foreground shadow-lg outline-none data-closed:animate-out data-closed:fade-out-0 data-closed:zoom-out-95 data-open:animate-in data-open:fade-in-0 data-open:zoom-in-95">
            {items.map((item) => (
              <Menu.Item
                key={item.label}
                disabled={item.disabled}
                onClick={item.onSelect}
                className={cn(ITEM, item.destructive ? "text-destructive [&_svg]:text-destructive" : "[&_svg]:text-muted-foreground")}
              >
                {item.icon && <item.icon />}
                {item.label}
              </Menu.Item>
            ))}
          </Menu.Popup>
        </Menu.Positioner>
      </Menu.Portal>
    </Menu.Root>
  )
}
