import type { ReactNode } from "react"
import { cn } from "@/lib/utils"

export interface ChoiceOption<T extends string> {
  id: T
  label: ReactNode
}

// 设置里的单选按钮组（性别、主题等）。onPick 带上被点的按钮，主题切换要从它的位置开始扩散
export function Choice<T extends string>({
  label,
  value,
  options,
  onPick,
  className,
}: {
  label: string
  value: T
  options: readonly ChoiceOption<T>[]
  onPick: (id: T, button: HTMLButtonElement) => void
  className?: string
}) {
  return (
    <div role="radiogroup" aria-label={label} className={cn("flex gap-2", className)}>
      {options.map((o) => (
        <button
          key={o.id}
          type="button"
          role="radio"
          aria-checked={value === o.id}
          onClick={(e) => onPick(o.id, e.currentTarget)}
          className={cn(
            "flex-1 rounded-lg border px-3 py-2 text-sm transition-colors outline-none focus-visible:ring-2 focus-visible:ring-ring",
            value === o.id ? "border-brand-strong/60 bg-brand/5 font-medium text-foreground" : "text-muted-foreground hover:bg-muted/60",
          )}
        >
          {o.label}
        </button>
      ))}
    </div>
  )
}
