import { motion, useReducedMotion } from "motion/react"
import { useUi } from "@/stores/ui"

const PARTICLES = 28
const COLORS = ["var(--brand)", "var(--gold)", "var(--brand-strong)", "#F7C873"]

// 成为好友、建群成功时的一次性粒子迸发（设计 3.6.7：全站唯一「热闹」的时刻）。
// 每次 celebrate() 换一个 key 重新挂载；开启「减少动态效果」时不播放，提示文字已经够了
export function Celebration() {
  const count = useUi((s) => s.celebration)
  const reduce = useReducedMotion()
  if (count === 0 || reduce) return null
  return <Burst key={count} />
}

function Burst() {
  return (
    <div aria-hidden="true" className="pointer-events-none fixed inset-0 z-[60] flex items-center justify-center">
      <div className="relative -translate-y-16">
        {Array.from({ length: PARTICLES }, (_, i) => {
          // 用序号算出的伪随机量，渲染保持纯函数
          const angle = (i / PARTICLES) * Math.PI * 2 + ((i * 37) % 11) / 20
          const distance = 110 + ((i * 53) % 90)
          const size = 6 + ((i * 29) % 6)
          const round = i % 3 !== 0
          return (
            <motion.span
              key={i}
              initial={{ x: 0, y: 0, scale: 0.4, opacity: 1, rotate: 0 }}
              animate={{
                x: Math.cos(angle) * distance,
                // 末段略微下坠，像被重力拉回
                y: [0, Math.sin(angle) * distance, Math.sin(angle) * distance + 40],
                scale: [0.4, 1, 0.8],
                opacity: [1, 1, 0],
                rotate: round ? 0 : 180 + ((i * 41) % 180),
              }}
              transition={{ duration: 1.1, ease: [0.16, 1, 0.3, 1], times: [0, 0.6, 1] }}
              className="absolute top-0 left-0"
              style={{
                width: size,
                height: round ? size : size * 0.5,
                borderRadius: round ? "9999px" : "2px",
                backgroundColor: COLORS[i % COLORS.length],
                marginLeft: -size / 2,
                marginTop: -size / 2,
              }}
            />
          )
        })}
      </div>
    </div>
  )
}
