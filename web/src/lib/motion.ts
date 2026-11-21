import type { Transition } from "motion/react"

// 全站只用这三组动效参数（设计 3.4.5），数值是初值，视觉原型阶段调整后写回设计文档。
export const springs = {
  // 按钮按下、徽章计数、勾选
  snappy: { type: "spring", stiffness: 500, damping: 32 },
  // 列表重排、面板与抽屉、气泡入场
  smooth: { type: "spring", stiffness: 260, damping: 28 },
  // 页面切换、登录页元素依次出现
  gentle: { type: "tween", duration: 0.35, ease: [0.22, 1, 0.36, 1] },
} as const satisfies Record<string, Transition>
