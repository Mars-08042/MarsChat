import { motion } from "motion/react"
import { Outlet, useLocation } from "react-router"
import { useAppearance } from "@/lib/appearance"
import { springs } from "@/lib/motion"
import { BrandPanel } from "./BrandPanel"

// 登录注册页固定深色（设计 3.4.3）：.dark 加在容器上，不受全站主题影响。
// 宽屏左右分屏；窄屏品牌区收成顶部横幅。
export function AuthLayout() {
  // 登录页背景是用户可选的外观偏好（设计 3.6.8）
  const background = useAppearance((s) => s.loginBackground)
  const { pathname } = useLocation()

  return (
    <div className="dark min-h-svh bg-background text-foreground lg:grid lg:grid-cols-[minmax(0,1.15fr)_minmax(30rem,1fr)]">
      <BrandPanel background={background} />
      <main className="flex items-center justify-center px-6 py-10 lg:py-16">
        <motion.div
          key={pathname}
          initial={{ opacity: 0, y: 12 }}
          animate={{ opacity: 1, y: 0 }}
          transition={springs.gentle}
          className="w-full max-w-sm"
        >
          <Outlet />
        </motion.div>
      </main>
    </div>
  )
}
