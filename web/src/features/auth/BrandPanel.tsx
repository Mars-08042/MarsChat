import BlurText from "@/components/effects/BlurText"
import RotatingText from "@/components/effects/RotatingText"
import { MarsLogo } from "@/components/common/MarsLogo"
import type { LoginBackground } from "@/lib/appearance"
import { BrandBackground } from "./BrandBackground"

// 轮播的卖点都对应后端已经实现的可靠性能力（设计 3.6.1）
const SELLING_POINTS = ["断线不丢消息", "多端实时同步", "发送幂等不重复", "已读回执精确到条"]

// 跑马灯内容取自协议里的真实名词，呼应 Rig AI 的工程感（设计 3.3）
const TICKER = [
  "WS 101 SWITCHING PROTOCOLS",
  "AUTH OK",
  "SEND_ACK seq=1024",
  "GET_DIFFERENCE → COMPLETE",
  "PEER_READ_SEQ 1023",
  "PING 30s",
  "CLIENT_MSG_ID DEDUP",
  "CLOSE 4003 → RESYNC",
  "KAFKA LAG 0",
]

export function BrandPanel({ background }: { background: LoginBackground }) {
  return (
    <section className="relative isolate flex h-60 flex-col justify-between overflow-hidden px-6 pt-6 pb-5 text-white sm:h-72 lg:h-auto lg:min-h-svh lg:px-12 lg:pt-10 lg:pb-8">
      <BrandBackground kind={background} />
      {/* 压暗底部和左上，保证文字在任何背景帧上都清晰 */}
      <div className="pointer-events-none absolute inset-0 -z-10 bg-linear-to-t from-[#0F0B0A]/85 via-[#0F0B0A]/15 to-[#0F0B0A]/45" />

      <header className="flex items-center gap-2.5">
        <MarsLogo className="size-8" />
        <span className="font-display text-lg font-semibold tracking-tight">MarsChat</span>
      </header>

      <div className="space-y-5 lg:space-y-7">
        <p className="hidden font-mono text-xs tracking-[0.25em] text-white/60 uppercase sm:block">
          Realtime messaging, engineered
        </p>
        <BlurText
          text="让每一条消息 都准时抵达"
          animateBy="letters"
          delay={60}
          className="font-display text-3xl leading-[1.15] font-semibold tracking-tight sm:text-4xl lg:text-6xl [&>span.inline-flex]:basis-full"
        />
        <div className="hidden items-center gap-3 text-lg text-white/80 sm:flex lg:text-2xl">
          <span>MarsChat ·</span>
          <RotatingText
            texts={SELLING_POINTS}
            rotationInterval={2600}
            staggerDuration={0.02}
            staggerFrom="last"
            mainClassName="overflow-hidden rounded-lg bg-[#E2452C] px-2.5 py-0.5 font-medium text-[#17110F]"
            splitLevelClassName="overflow-hidden pb-0.5"
          />
        </div>
      </div>

      <Ticker />
    </section>
  )
}

function Ticker() {
  const line = TICKER.join("   ·   ")
  return (
    <div className="hidden overflow-hidden border-t border-white/10 pt-4 font-mono text-[11px] tracking-widest whitespace-pre text-white/45 lg:block">
      {/* 内容重复两份，动画平移一半宽度后无缝衔接；减少动态效果时停住 */}
      <div className="flex w-max animate-marquee motion-reduce:animate-none">
        <span className="pr-[3ch]">{line}</span>
        <span className="pr-[3ch]" aria-hidden="true">
          {line}
        </span>
      </div>
    </div>
  )
}
