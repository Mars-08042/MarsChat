import { motion } from "motion/react"

// 没有打开会话时的空状态：低频场景，允许稍微炫一点（设计 3.4.1）
export function EmptyChat() {
  return (
    <div className="relative flex flex-1 flex-col items-center justify-center gap-8 overflow-hidden p-8">
      {/* 背景的火星红光晕 */}
      <div
        aria-hidden="true"
        className="pointer-events-none absolute top-1/2 left-1/2 size-[36rem] -translate-x-1/2 -translate-y-1/2 rounded-full opacity-60 blur-3xl dark:opacity-40"
        style={{ background: "radial-gradient(circle, rgb(226 69 44 / 0.18), transparent 65%)" }}
      />
      <div aria-hidden="true" className="relative size-44">
        {/* 轨道和卫星 */}
        <div className="absolute inset-0 animate-[spin_36s_linear_infinite] rounded-full border border-dashed border-brand/35 motion-reduce:animate-none">
          <span className="absolute top-1/2 -right-1.5 size-3 rounded-full bg-gold shadow-[0_0_12px_rgb(240_162_59/0.8)]" />
        </div>
        <motion.div
          animate={{ y: [0, -6, 0] }}
          transition={{ duration: 6, ease: "easeInOut", repeat: Infinity }}
          className="absolute inset-7 rounded-full shadow-[0_24px_60px_-12px_rgb(226_69_44/0.55)]"
          style={{ background: "radial-gradient(circle at 32% 28%, #FFB199, #E2452C 45%, #7A1E10)" }}
        >
          <span className="absolute top-[30%] right-[24%] size-5 rounded-full bg-[#7A1E10]/30" />
          <span className="absolute bottom-[26%] left-[28%] size-3.5 rounded-full bg-[#7A1E10]/25" />
          <span className="absolute right-[30%] bottom-[20%] size-2 rounded-full bg-[#7A1E10]/25" />
        </motion.div>
      </div>
      <div className="relative space-y-2 text-center">
        <h2 className="font-display text-xl font-semibold tracking-tight">选一个会话，开始聊天</h2>
        <p className="text-sm text-muted-foreground">消息会实时同步到你登录的每一台设备</p>
      </div>
    </div>
  )
}
