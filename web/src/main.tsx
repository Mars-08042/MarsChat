import { StrictMode } from "react"
import { createRoot } from "react-dom/client"
import { MotionConfig } from "motion/react"
import { App } from "@/App"
import { springs } from "@/lib/motion"
import "@/index.css"

const root = document.getElementById("root")
if (!root) throw new Error("missing #root element")

createRoot(root).render(
  <StrictMode>
    {/* reducedMotion="user"：系统开启“减少动态效果”时关闭位移与 layout 动画，保留透明度和颜色动画 */}
    <MotionConfig reducedMotion="user" transition={springs.smooth}>
      <App />
    </MotionConfig>
  </StrictMode>,
)
