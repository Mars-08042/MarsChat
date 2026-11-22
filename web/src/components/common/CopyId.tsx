import { useEffect, useState } from "react"
import { AnimatePresence, motion } from "motion/react"
import { Check, Copy } from "lucide-react"
import { toast } from "sonner"
import { springs } from "@/lib/motion"
import { cn } from "@/lib/utils"

const COPIED_MS = 1500

// 用户 ID、群 ID：等宽字体，点击复制，复制后图标换成勾（设计 3.6.7）
export function CopyId({ id, label = "ID", className }: { id: string; label?: string; className?: string }) {
  const [copied, setCopied] = useState(false)

  useEffect(() => {
    if (!copied) return
    const timer = setTimeout(() => setCopied(false), COPIED_MS)
    return () => clearTimeout(timer)
  }, [copied])

  const copy = async () => {
    try {
      // 只有安全上下文（localhost、HTTPS）才有 clipboard
      await navigator.clipboard.writeText(id)
      setCopied(true)
    } catch {
      toast("复制失败，请手动选择 ID 复制")
    }
  }

  return (
    <button
      type="button"
      onClick={() => void copy()}
      aria-label={`复制${label} ${id}`}
      className={cn(
        "group inline-flex max-w-full items-center gap-1.5 rounded-md px-1.5 py-0.5 font-mono text-xs text-muted-foreground transition-colors outline-none hover:bg-muted hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50",
        className,
      )}
    >
      <span className="truncate select-all">
        {label} {id}
      </span>
      <span className="relative inline-flex size-3.5 shrink-0">
        <AnimatePresence mode="popLayout" initial={false}>
          <motion.span
            key={copied ? "check" : "copy"}
            initial={{ scale: 0.4, opacity: 0 }}
            animate={{ scale: 1, opacity: 1 }}
            exit={{ scale: 0.4, opacity: 0 }}
            transition={springs.snappy}
            className="absolute inset-0"
          >
            {copied ? <Check className="size-3.5 text-success" strokeWidth={2.5} /> : <Copy className="size-3.5" />}
          </motion.span>
        </AnimatePresence>
      </span>
      <span role="status" className="sr-only">
        {copied ? "已复制" : ""}
      </span>
    </button>
  )
}
