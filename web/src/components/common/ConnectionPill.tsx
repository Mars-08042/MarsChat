import { AnimatePresence, motion } from "motion/react"
import { useEffect, useState, type ReactNode } from "react"
import { Check, LoaderCircle, ServerCog, WifiOff } from "lucide-react"
import { springs } from "@/lib/motion"
import { useSecondsUntil } from "@/lib/use-countdown"
import { cn } from "@/lib/utils"

// 连接状态（设计 3.6.5）。第 4 阶段由连接状态机产生，原型阶段由演示面板手动切换
export type ConnectionStatus =
  | { kind: "connecting" }
  | { kind: "syncing"; count: number }
  | { kind: "synced" }
  | { kind: "reconnecting"; retryAt: number }
  | { kind: "offline" }
  | { kind: "maintenance" }

// 「已同步」停留多久后收成小圆点
const SYNCED_HOLD_MS = 1500

interface ConnectionPillProps {
  status: ConnectionStatus
  // 重连等待时点击胶囊立即重连
  onRetryNow?: () => void
}

export function ConnectionPill({ status, onRetryNow }: ConnectionPillProps) {
  // 记录已经收起的那一次 synced 状态；状态对象换了就重新展开
  const [collapsedFor, setCollapsedFor] = useState<ConnectionStatus | null>(null)
  useEffect(() => {
    if (status.kind !== "synced") return
    const timer = setTimeout(() => setCollapsedFor(status), SYNCED_HOLD_MS)
    return () => clearTimeout(timer)
  }, [status])
  const collapsed = status.kind === "synced" && collapsedFor === status

  const clickable = status.kind === "reconnecting" && onRetryNow !== undefined
  const Root = clickable ? motion.button : motion.div

  return (
    <Root
      layout
      type={clickable ? "button" : undefined}
      onClick={clickable ? onRetryNow : undefined}
      role={clickable ? undefined : "status"}
      aria-live="polite"
      title={collapsed ? "已连接" : undefined}
      transition={springs.smooth}
      // 灵动岛式的胶囊在两套主题下都用近黑色；圆角写在 style 里，layout 动画才会校正圆角形变
      style={{ borderRadius: 999 }}
      className={cn(
        "flex items-center justify-center overflow-hidden bg-[#17110F] text-[#F5EFE9] shadow-float ring-1 ring-white/10",
        collapsed ? "size-3.5" : "h-8 px-3.5 text-xs font-medium",
        clickable && "cursor-pointer hover:bg-[#2A1F1B]",
      )}
    >
      <AnimatePresence mode="popLayout" initial={false}>
        <motion.span
          key={collapsed ? "dot" : status.kind}
          initial={{ opacity: 0, filter: "blur(4px)" }}
          animate={{ opacity: 1, filter: "blur(0px)" }}
          exit={{ opacity: 0, filter: "blur(4px)" }}
          transition={springs.gentle}
          className="flex items-center gap-2 whitespace-nowrap"
        >
          {collapsed ? (
            <>
              <span className="size-1.5 rounded-full bg-[#3FBF86] shadow-[0_0_6px_#3FBF86]" />
              <span className="sr-only">已连接</span>
            </>
          ) : (
            <PillContent status={status} />
          )}
        </motion.span>
      </AnimatePresence>
    </Root>
  )
}

function PillContent({ status }: { status: ConnectionStatus }): ReactNode {
  switch (status.kind) {
    case "connecting":
      return (
        <>
          <PulseDot />
          连接中…
        </>
      )
    case "syncing":
      return (
        <>
          <LoaderCircle className="size-3.5 animate-spin text-[#FF6A4D] motion-reduce:animate-none" />
          <span>
            正在同步 <AnimatedCount value={status.count} /> 条消息
          </span>
        </>
      )
    case "synced":
      return (
        <>
          <Check className="size-3.5 text-[#3FBF86]" strokeWidth={3} />
          已同步
        </>
      )
    case "reconnecting":
      return (
        <>
          <span className="size-1.5 rounded-full bg-[#F0A23B]" />
          {/* 每次进入重连等待都换一个 retryAt，用 key 重新挂载倒计时 */}
          <RetryCountdown key={status.retryAt} retryAt={status.retryAt} />
          <span className="text-[#A89C94]">· 立即重连</span>
        </>
      )
    case "offline":
      return (
        <>
          <WifiOff className="size-3.5 text-[#F0A23B]" />
          网络已断开
        </>
      )
    case "maintenance":
      return (
        <>
          <ServerCog className="size-3.5 text-[#F0A23B]" />
          服务器维护，正在切换…
        </>
      )
  }
}

function PulseDot() {
  return (
    <span className="relative flex size-2">
      <span className="absolute inline-flex size-full animate-ping rounded-full bg-[#FF6A4D] opacity-75 motion-reduce:animate-none" />
      <span className="relative inline-flex size-2 rounded-full bg-[#FF6A4D]" />
    </span>
  )
}

function RetryCountdown({ retryAt }: { retryAt: number }) {
  const seconds = useSecondsUntil(retryAt)
  return (
    <span>
      连接已断开，<AnimatedCount value={seconds} /> 秒后重连
    </span>
  )
}

// 数字变化时从下方滑入，和胶囊本身的变形节奏一致
function AnimatedCount({ value }: { value: number }) {
  return (
    <span className="inline-flex overflow-hidden align-bottom font-mono tabular-nums">
      <AnimatePresence mode="popLayout" initial={false}>
        <motion.span
          key={value}
          initial={{ y: "100%", opacity: 0 }}
          animate={{ y: 0, opacity: 1 }}
          exit={{ y: "-100%", opacity: 0 }}
          transition={springs.snappy}
        >
          {value}
        </motion.span>
      </AnimatePresence>
    </span>
  )
}
