import { useReducedMotion } from "motion/react"
import Grainient from "@/components/effects/Grainient"
import Silk from "@/components/effects/Silk"
import SoftAurora from "@/components/effects/SoftAurora"
import type { LoginBackground } from "@/lib/appearance"

// 登录注册页品牌区的背景（设计 3.4.5）。
// 静态渐变始终垫在最底层：浏览器不支持 WebGL 时画布是透明的，露出渐变；
// 开启「减少动态效果」时不挂载 WebGL 组件，只保留静态渐变。
export function BrandBackground({ kind }: { kind: LoginBackground }) {
  const reduceMotion = useReducedMotion()
  const animated = !reduceMotion && kind !== "static"

  return (
    <div aria-hidden="true" className="absolute inset-0 -z-10 bg-[#0F0B0A]">
      <StaticGlow />
      {animated && kind === "soft-aurora" && (
        <SoftAurora color1="#FFB199" color2="#E2452C" speed={0.5} brightness={1.1} bandHeight={0.55} />
      )}
      {animated && kind === "grainient" && (
        <Grainient
          colors={["#0F0B0A", "#3A120A", "#9A2A16", "#E2452C", "#FF8A66"]}
          speed={0.6}
          grainAmount={0.09}
          dpr={1.5}
        />
      )}
      {animated && kind === "silk" && <Silk color="#8C2F1B" sheenColor="#FF8A66" speed={3} dpr={1.5} />}
    </div>
  )
}

function StaticGlow() {
  return (
    <div
      className="absolute inset-0"
      style={{
        backgroundImage: [
          "radial-gradient(110% 70% at 10% 105%, rgb(226 69 44 / 0.9), transparent 62%)",
          "radial-gradient(70% 55% at 85% 95%, rgb(194 54 31 / 0.55), transparent 70%)",
          "radial-gradient(60% 45% at 90% 5%, rgb(240 162 59 / 0.22), transparent 70%)",
        ].join(", "),
      }}
    />
  )
}
