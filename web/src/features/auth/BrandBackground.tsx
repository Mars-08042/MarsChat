import { lazy, Suspense } from "react"
import { useReducedMotion } from "motion/react"
import type { LoginBackground } from "@/lib/appearance"

// 三个 WebGL 背景各自懒加载，只下载用户选中的那个（设计 2.3）
const Grainient = lazy(() => import("@/components/effects/Grainient"))
const Silk = lazy(() => import("@/components/effects/Silk"))
const SoftAurora = lazy(() => import("@/components/effects/SoftAurora"))

// 登录注册页品牌区的背景（设计 3.4.5）。
// 静态渐变始终垫在最底层：浏览器不支持 WebGL、背景代码还在下载时都露出渐变；
// 开启「减少动态效果」时不挂载 WebGL 组件，也不下载它们，只保留静态渐变。
export function BrandBackground({ kind }: { kind: LoginBackground }) {
  const reduceMotion = useReducedMotion()
  const animated = !reduceMotion && kind !== "static"

  return (
    <div aria-hidden="true" className="absolute inset-0 -z-10 bg-[#0F0B0A]">
      <StaticGlow />
      <Suspense fallback={null}>
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
      </Suspense>
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
