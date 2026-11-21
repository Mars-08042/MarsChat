import { useCallback, useEffect, useState } from "react"

const TICK_MS = 250

function secondsBetween(now: number, deadline: number): number {
  return Math.max(0, Math.ceil((deadline - now) / 1000))
}

// 由用户操作发起的倒计时，例如验证码的「重新发送」。
// start 在事件处理函数里调用，起点和截止时间同时确定，首帧不会用到过期的当前时间。
export function useCountdown(): { secondsLeft: number; start: (seconds: number) => void } {
  const [clock, setClock] = useState<{ now: number; deadline: number } | null>(null)

  const start = useCallback((seconds: number) => {
    const now = Date.now()
    setClock({ now, deadline: now + seconds * 1000 })
  }, [])

  useEffect(() => {
    if (!clock || clock.now >= clock.deadline) return
    const timer = setTimeout(() => setClock((c) => c && { ...c, now: Date.now() }), TICK_MS)
    return () => clearTimeout(timer)
  }, [clock])

  return { secondsLeft: clock ? secondsBetween(clock.now, clock.deadline) : 0, start }
}

// 倒计时到外部给定的截止时间（例如重连时刻）。
// 截止时间变化时调用方要用 key 重新挂载组件，否则首帧会用挂载时记录的旧时间计算。
export function useSecondsUntil(deadline: number): number {
  const [now, setNow] = useState(() => Date.now())
  const done = now >= deadline

  useEffect(() => {
    if (done) return
    const timer = setInterval(() => setNow(Date.now()), TICK_MS)
    return () => clearInterval(timer)
  }, [done])

  return secondsBetween(now, deadline)
}

// 每分钟刷新一次的当前时间，用于「今天」「昨天」这类相对时间的显示
export function useNow(intervalMs = 60_000): number {
  const [now, setNow] = useState(() => Date.now())

  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), intervalMs)
    return () => clearInterval(timer)
  }, [intervalMs])

  return now
}
