import { useEffect, useRef, type ReactNode } from "react"
import { AnimatePresence, motion } from "motion/react"
import { CircleExclamationPoint, Maximize2, Mic, MicOff, Minimize2, Phone, PhoneOff, Video, VideoOff } from "lucide-react"
import { UserAvatar } from "@/components/common/UserAvatar"
import { springs } from "@/lib/motion"
import { formatDuration } from "@/lib/time"
import { useNow } from "@/lib/use-countdown"
import { cn } from "@/lib/utils"
import type { Call } from "@/realtime/call/engine"
import { callActions } from "@/realtime/client"
import { useCall } from "@/stores/call"
import { useIncomingCallAlerts } from "./alerts"
import { endReasonText } from "./call-text"

// 通话层挂在登录后的主框架上，切换页面时不中断（设计 3.6.10）。
// 全屏界面与右下角悬浮窗共用一个 layoutId，最小化和展开时整块变形过渡。
export function CallLayer() {
  const call = useCall((s) => s.call)
  const constraintsRef = useRef<HTMLDivElement>(null)
  useIncomingCallAlerts(call)

  return (
    <>
      {/* 对方的声音由这里播放，不随全屏界面和悬浮窗切换而中断；视频元素一律静音 */}
      {call?.remoteStream && <StreamMedia kind="audio" stream={call.remoteStream} />}
      {/* 悬浮窗的拖动范围 */}
      <div ref={constraintsRef} className="pointer-events-none fixed inset-4 z-40" />
      <AnimatePresence>
        {call?.phase.kind === "incoming" && <IncomingCallCard key={call.id} call={call} />}
      </AnimatePresence>
      <AnimatePresence>
        {call && call.phase.kind !== "incoming" && !call.minimized && <CallScreen key={`${call.id}-full`} call={call} />}
      </AnimatePresence>
      <AnimatePresence>
        {call && call.minimized && <MiniCall key={`${call.id}-mini`} call={call} constraintsRef={constraintsRef} />}
      </AnimatePresence>
      <NoticeToast />
    </>
  )
}

// 把 MediaStream 接到 <audio> / <video> 上。srcObject 只能在 DOM 上设置
function StreamMedia({ kind, stream, mirrored, className }: { kind: "audio" | "video"; stream: MediaStream; mirrored?: boolean; className?: string }) {
  const ref = useRef<HTMLMediaElement | null>(null)
  useEffect(() => {
    if (ref.current) ref.current.srcObject = stream
  }, [stream])
  if (kind === "audio") return <audio ref={(el) => void (ref.current = el)} autoPlay />
  return (
    <video
      ref={(el) => void (ref.current = el)}
      autoPlay
      playsInline
      muted
      className={cn("size-full object-cover", mirrored && "-scale-x-100", className)}
    />
  )
}

function statusText(call: Call, now: number): ReactNode {
  const { phase } = call
  switch (phase.kind) {
    case "dialing":
      return "正在呼叫…"
    case "outgoing":
      return (
        <>
          正在等待对方接听 <span className="font-mono tabular-nums">{formatDuration(now - phase.since)}</span>
        </>
      )
    case "incoming":
      return call.media === "video" ? "邀请你视频通话" : "邀请你语音通话"
    case "connecting":
      return "正在建立连接…"
    case "active":
      return <span className="font-mono text-base tabular-nums">{formatDuration(now - phase.since)}</span>
    case "ended":
      return (
        <>
          {endReasonText(phase.reason, call)}
          {phase.duration !== undefined && <span className="font-mono tabular-nums"> · {formatDuration(phase.duration)}</span>}
        </>
      )
  }
}

// 振铃时头像外一圈圈扩散的火星红波纹；减少动态效果时不显示
function Ripple({ active, className }: { active: boolean; className?: string }) {
  if (!active) return null
  return (
    <span aria-hidden="true" className={cn("pointer-events-none absolute inset-0 motion-reduce:hidden", className)}>
      {[0, 1, 2].map((i) => (
        <motion.span
          key={i}
          className="absolute inset-0 rounded-full border-2 border-[#FF6A4D]"
          initial={{ scale: 1, opacity: 0.55 }}
          animate={{ scale: 2.1, opacity: 0 }}
          transition={{ duration: 2.4, ease: "easeOut", repeat: Infinity, delay: i * 0.8 }}
        />
      ))}
    </span>
  )
}

function CallScreen({ call }: { call: Call }) {
  const now = useNow(1000)
  const { hangup, toggleMute, toggleCamera, setMinimized } = callActions
  const ended = call.phase.kind === "ended"
  const ringing = call.phase.kind === "outgoing" || call.phase.kind === "dialing"
  const remote = call.media === "video" && call.phase.kind === "active" ? call.remoteStream : undefined
  const showVideo = remote !== undefined

  return (
    <motion.div
      layoutId={`call-surface-${call.id}`}
      initial={{ opacity: 0 }}
      animate={{ opacity: 1 }}
      exit={{ opacity: 0 }}
      transition={springs.smooth}
      style={{ borderRadius: 0 }}
      role="dialog"
      aria-label={`与 ${call.peer.nickname} 通话`}
      className="dark fixed inset-0 z-50 flex flex-col overflow-hidden bg-[#0F0B0A] text-[#F5EFE9]"
    >
      {/* 背景：视频通话接通后是对方画面，其余时候是火星红光晕。cameraOff 是本端摄像头，不影响对方画面 */}
      {remote ? <RemoteVideo stream={remote} /> : <CallBackdrop />}

      <div className="relative flex items-center justify-between px-6 pt-5">
        <span className="font-mono text-xs tracking-[0.2em] text-white/50 uppercase">
          {call.media === "video" ? "Video call" : "Voice call"} · P2P
        </span>
        {!ended && (
          <button
            type="button"
            onClick={() => setMinimized(true)}
            aria-label="最小化"
            className="flex size-10 items-center justify-center rounded-full text-white/80 transition-colors hover:bg-white/10"
          >
            <Minimize2 className="size-5" />
          </button>
        )}
      </div>

      {/* 视频接通后名字和计时移到顶部，不挡住对方画面中央 */}
      <div
        className={cn(
          "relative flex flex-1 flex-col items-center gap-6 px-6 text-center",
          showVideo ? "justify-start [text-shadow:0_1px_8px_rgb(0_0_0/0.5)]" : "justify-center",
        )}
      >
        {!showVideo && (
          <div className="relative size-32">
            <Ripple active={ringing} />
            <UserAvatar seed={call.peer.id} name={call.peer.nickname} avatarId={call.peer.avatarId} size="hero" className="ring-4 ring-white/10" />
          </div>
        )}
        <div className="space-y-2" aria-live="polite">
          <h2 className={cn("font-display font-semibold tracking-tight", showVideo ? "text-xl" : "text-3xl")}>{call.peer.nickname}</h2>
          <p className={cn("text-sm", ended ? "text-[#FF8A80]" : "text-white/70")}>{statusText(call, now)}</p>
        </div>
      </div>

      {showVideo && <LocalPreview stream={call.localStream} cameraOff={call.cameraOff} />}

      <div className="relative flex items-center justify-center gap-5 pb-10">
        <ControlButton
          label={call.muted ? "取消静音" : "静音"}
          active={call.muted}
          disabled={ended}
          onClick={toggleMute}
          icon={call.muted ? <MicOff className="size-6" /> : <Mic className="size-6" />}
        />
        {call.media === "video" && (
          <ControlButton
            label={call.cameraOff ? "打开摄像头" : "关闭摄像头"}
            active={call.cameraOff}
            disabled={ended}
            onClick={toggleCamera}
            icon={call.cameraOff ? <VideoOff className="size-6" /> : <Video className="size-6" />}
          />
        )}
        <motion.button
          type="button"
          onClick={hangup}
          disabled={ended}
          whileTap={{ scale: 0.9 }}
          transition={springs.snappy}
          aria-label={call.connected ? "挂断" : "取消呼叫"}
          className="flex size-16 items-center justify-center rounded-full bg-[#C2361F] text-white shadow-[0_12px_32px_-6px_rgb(194_54_31/0.7)] transition-colors hover:bg-[#A52D19] disabled:opacity-40"
        >
          <PhoneOff className="size-7" />
        </motion.button>
      </div>
    </motion.div>
  )
}

function CallBackdrop() {
  return (
    <div
      aria-hidden="true"
      className="absolute inset-0"
      style={{
        backgroundImage: [
          "radial-gradient(60% 50% at 50% 38%, rgb(226 69 44 / 0.35), transparent 70%)",
          "radial-gradient(90% 60% at 50% 120%, rgb(194 54 31 / 0.45), transparent 70%)",
        ].join(", "),
      }}
    />
  )
}

// 对方画面铺满全屏；顶部加一层渐暗，名字和时长才看得清
function RemoteVideo({ stream }: { stream: MediaStream }) {
  return (
    <div aria-hidden="true" className="absolute inset-0 overflow-hidden bg-black">
      <StreamMedia kind="video" stream={stream} />
      <div className="absolute inset-x-0 top-0 h-40 bg-gradient-to-b from-black/50 to-transparent" />
    </div>
  )
}

// 自己的画面镜像显示，和照镜子一致；关闭摄像头时显示图标
function LocalPreview({ stream, cameraOff }: { stream?: MediaStream; cameraOff: boolean }) {
  return (
    <div className="absolute right-6 bottom-32 flex h-44 w-32 items-center justify-center overflow-hidden rounded-2xl border border-white/15 bg-[#1A1412] shadow-float">
      {cameraOff || !stream ? <VideoOff className="size-6 text-white/50" /> : <StreamMedia kind="video" stream={stream} mirrored />}
    </div>
  )
}

function ControlButton({
  label,
  icon,
  active,
  disabled,
  onClick,
}: {
  label: string
  icon: ReactNode
  active: boolean
  disabled?: boolean
  onClick: () => void
}) {
  return (
    <motion.button
      type="button"
      onClick={onClick}
      disabled={disabled}
      whileTap={{ scale: 0.9 }}
      transition={springs.snappy}
      aria-label={label}
      aria-pressed={active}
      className={cn(
        "flex size-14 items-center justify-center rounded-full backdrop-blur-md transition-colors disabled:opacity-40",
        active ? "bg-white text-[#17110F]" : "bg-white/12 text-white hover:bg-white/20",
      )}
    >
      {icon}
    </motion.button>
  )
}

// 来电卡片：右上角滑入，接听或拒绝；多端同时响铃，其他设备接听后这里收到「已在其他设备接听」
function IncomingCallCard({ call }: { call: Call }) {
  const { accept, reject } = callActions
  const now = useNow(1000)

  return (
    <motion.div
      initial={{ opacity: 0, y: -24, scale: 0.96 }}
      animate={{ opacity: 1, y: 0, scale: 1 }}
      exit={{ opacity: 0, y: -16, scale: 0.96 }}
      transition={springs.smooth}
      role="alertdialog"
      aria-label={`${call.peer.nickname} 来电`}
      className="dark fixed top-4 right-4 z-[60] w-[22rem] max-w-[calc(100vw-2rem)] overflow-hidden rounded-card border border-white/10 bg-[#1A1412] p-4 text-[#F5EFE9] shadow-float"
    >
      <div
        aria-hidden="true"
        className="pointer-events-none absolute inset-0"
        style={{ background: "radial-gradient(80% 120% at 0% 0%, rgb(226 69 44 / 0.28), transparent 60%)" }}
      />
      <div className="relative flex items-center gap-4">
        <div className="relative size-14 shrink-0">
          <Ripple active />
          <UserAvatar seed={call.peer.id} name={call.peer.nickname} avatarId={call.peer.avatarId} size="xl" />
        </div>
        <div className="min-w-0 flex-1">
          <p className="truncate font-display text-lg font-semibold">{call.peer.nickname}</p>
          <p className="text-sm text-white/70">{statusText(call, now)}</p>
        </div>
      </div>
      <div className="relative mt-4 grid grid-cols-2 gap-2.5">
        <button
          type="button"
          onClick={reject}
          className="flex h-11 items-center justify-center gap-2 rounded-xl bg-[#C2361F] font-medium text-white transition-colors hover:bg-[#A52D19]"
        >
          <PhoneOff className="size-4" />
          拒绝
        </button>
        <button
          type="button"
          onClick={accept}
          className="flex h-11 items-center justify-center gap-2 rounded-xl bg-[#187A4C] font-medium text-white transition-colors hover:bg-[#136640]"
        >
          {call.media === "video" ? <Video className="size-4" /> : <Phone className="size-4" />}
          接听
        </button>
      </div>
    </motion.div>
  )
}

// 最小化后的悬浮窗：可以拖动，显示时长、静音、挂断，点击展开
function MiniCall({ call, constraintsRef }: { call: Call; constraintsRef: React.RefObject<HTMLDivElement | null> }) {
  const now = useNow(1000)
  const { hangup, toggleMute, setMinimized } = callActions

  return (
    <motion.div
      layoutId={`call-surface-${call.id}`}
      drag
      dragConstraints={constraintsRef}
      dragMomentum={false}
      initial={{ opacity: 0 }}
      animate={{ opacity: 1 }}
      exit={{ opacity: 0 }}
      transition={springs.smooth}
      style={{ borderRadius: 20 }}
      className="dark fixed right-6 bottom-6 z-50 flex w-72 cursor-grab items-center gap-3 border border-white/10 bg-[#1A1412] p-3 text-[#F5EFE9] shadow-float active:cursor-grabbing"
    >
      <UserAvatar seed={call.peer.id} name={call.peer.nickname} avatarId={call.peer.avatarId} />
      <div className="min-w-0 flex-1">
        <p className="truncate text-sm font-medium">{call.peer.nickname}</p>
        <p className="text-xs text-[#3FBF86]">{statusText(call, now)}</p>
      </div>
      <button
        type="button"
        onClick={toggleMute}
        aria-label={call.muted ? "取消静音" : "静音"}
        aria-pressed={call.muted}
        className={cn("flex size-9 items-center justify-center rounded-full transition-colors", call.muted ? "bg-white text-[#17110F]" : "hover:bg-white/10")}
      >
        {call.muted ? <MicOff className="size-4" /> : <Mic className="size-4" />}
      </button>
      <button
        type="button"
        onClick={() => setMinimized(false)}
        aria-label="展开通话"
        className="flex size-9 items-center justify-center rounded-full transition-colors hover:bg-white/10"
      >
        <Maximize2 className="size-4" />
      </button>
      <button
        type="button"
        onClick={hangup}
        aria-label="挂断"
        className="flex size-9 items-center justify-center rounded-full bg-[#C2361F] text-white transition-colors hover:bg-[#A52D19]"
      >
        <PhoneOff className="size-4" />
      </button>
    </motion.div>
  )
}

// 没有进入通话界面的结果（呼叫被拒绝、设备不可用、来电在本端接听前结束）在页面顶部提示，4 秒后消失
function NoticeToast() {
  const error = useCall((s) => s.notice)

  useEffect(() => {
    if (!error) return
    const timer = setTimeout(callActions.dismissNotice, 4000)
    return () => clearTimeout(timer)
  }, [error])

  return (
    <AnimatePresence>
      {error && (
        <motion.div
          key={error.at}
          initial={{ opacity: 0, y: -16 }}
          animate={{ opacity: 1, y: 0 }}
          exit={{ opacity: 0, y: -16 }}
          transition={springs.smooth}
          role="alert"
          className="fixed top-4 left-1/2 z-[60] flex -translate-x-1/2 items-center gap-2 rounded-full bg-[#17110F] px-4 py-2.5 text-sm text-[#F5EFE9] shadow-float ring-1 ring-white/10"
        >
          <CircleExclamationPoint className="size-4 text-[#F0A23B]" />
          {error.text}
        </motion.div>
      )}
    </AnimatePresence>
  )
}
