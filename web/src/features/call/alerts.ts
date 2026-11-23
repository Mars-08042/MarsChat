// 来电提醒（设计 3.6.10）：铃声、标签页在后台时标题闪烁、已授权时的系统通知。
// 都只是辅助：来电卡片本身不依赖它们，任何一项不可用时静默跳过。

import { useEffect } from "react"
import type { Call } from "@/realtime/call/engine"

// 两声短促的提示音，每 2 秒一组。用 Web Audio 合成，不需要音频文件。
// 自动播放限制：用户还没和页面交互过时 AudioContext 处于 suspended，resume 会失败，铃声不响
function playRingtone(): () => void {
  if (typeof AudioContext === "undefined") return () => {}
  let ctx: AudioContext
  try {
    ctx = new AudioContext()
  } catch {
    return () => {}
  }
  ctx.resume().catch(() => {})
  const ring = () => {
    if (ctx.state !== "running") return
    const t0 = ctx.currentTime
    for (const [offset, frequency] of [
      [0, 784],
      [0.32, 1047],
    ]) {
      const osc = ctx.createOscillator()
      const gain = ctx.createGain()
      osc.type = "sine"
      osc.frequency.value = frequency
      gain.gain.setValueAtTime(0, t0 + offset)
      gain.gain.linearRampToValueAtTime(0.12, t0 + offset + 0.02)
      gain.gain.linearRampToValueAtTime(0, t0 + offset + 0.26)
      osc.connect(gain).connect(ctx.destination)
      osc.start(t0 + offset)
      osc.stop(t0 + offset + 0.3)
    }
  }
  const first = setTimeout(ring, 50)
  const timer = setInterval(ring, 2000)
  return () => {
    clearTimeout(first)
    clearInterval(timer)
    ctx.close().catch(() => {})
  }
}

// 标签页在后台时，标题在「来电：昵称」和原标题之间每秒切换一次；回到前台时恢复。
// 原标题由未读数维护（app/use-unread-total.ts），闪烁期间它变了也以最新的为准
function flashTitle(text: string): () => void {
  let base = document.title
  let timer: ReturnType<typeof setInterval> | undefined
  const restore = () => {
    if (document.title === text) document.title = base
  }
  const tick = () => {
    if (document.title === text) {
      document.title = base
    } else {
      base = document.title
      document.title = text
    }
  }
  const update = () => {
    clearInterval(timer)
    timer = undefined
    restore()
    if (document.hidden) {
      tick()
      timer = setInterval(tick, 1000)
    }
  }
  update()
  document.addEventListener("visibilitychange", update)
  return () => {
    document.removeEventListener("visibilitychange", update)
    clearInterval(timer)
    restore()
  }
}

// 只在已授权且标签页在后台时发系统通知；点击通知回到页面
function notify(name: string, media: Call["media"]): Notification | undefined {
  if (typeof Notification === "undefined" || Notification.permission !== "granted" || !document.hidden) return undefined
  try {
    const n = new Notification(`来电：${name}`, {
      body: media === "video" ? "邀请你视频通话" : "邀请你语音通话",
      tag: "marschat-call",
      requireInteraction: true,
    })
    n.onclick = () => {
      window.focus()
      n.close()
    }
    return n
  } catch {
    // 部分浏览器（例如安卓 Chrome）只允许经 Service Worker 发通知
    return undefined
  }
}

export function useIncomingCallAlerts(call: Call | null): void {
  const ringing = call?.phase.kind === "incoming" ? call : null
  const id = ringing?.id
  const name = ringing?.peer.nickname
  const media = ringing?.media

  // 铃声只跟随来电本身，取到来电方资料、名字变化时不重新响
  useEffect(() => {
    if (!id) return
    return playRingtone()
  }, [id])

  useEffect(() => {
    if (!id || !name || !media) return
    const stopFlash = flashTitle(`来电：${name}`)
    const notification = notify(name, media)
    return () => {
      stopFlash()
      notification?.close()
    }
  }, [id, name, media])
}
