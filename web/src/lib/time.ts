// 消息与会话列表的时间显示（设计 3.6.2、3.6.3）。全部按浏览器本地时区计算。

const DAY_MS = 86_400_000
const WEEKDAYS = ["周日", "周一", "周二", "周三", "周四", "周五", "周六"]

function startOfDay(ts: number): number {
  const d = new Date(ts)
  d.setHours(0, 0, 0, 0)
  return d.getTime()
}

// 两个时间相差的自然日数。用 round 抵消夏令时切换当天 23 或 25 小时的偏差
function dayDiff(ts: number, now: number): number {
  return Math.round((startOfDay(now) - startOfDay(ts)) / DAY_MS)
}

export function isSameDay(a: number, b: number): boolean {
  return startOfDay(a) === startOfDay(b)
}

export function formatClock(ts: number): string {
  const d = new Date(ts)
  return `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`
}

// 管理页等表格里的日期：2026-11-22
export function formatDate(ts: number): string {
  const d = new Date(ts)
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`
}

// 会话列表：今天显示时刻，昨天显示「昨天」，一周内显示星期，今年显示月/日，更早带年份
export function formatListTime(ts: number, now: number): string {
  const diff = dayDiff(ts, now)
  const d = new Date(ts)
  if (diff <= 0) return formatClock(ts)
  if (diff === 1) return "昨天"
  if (diff < 7) return WEEKDAYS[d.getDay()]
  if (d.getFullYear() === new Date(now).getFullYear()) return `${d.getMonth() + 1}/${d.getDate()}`
  return `${d.getFullYear()}/${d.getMonth() + 1}/${d.getDate()}`
}

// 通话时长：不到一小时显示 03:21，超过一小时显示 1:03:21
export function formatDuration(ms: number): string {
  const total = Math.max(0, Math.floor(ms / 1000))
  const h = Math.floor(total / 3600)
  const m = Math.floor((total % 3600) / 60)
  const s = total % 60
  const mmss = `${String(m).padStart(2, "0")}:${String(s).padStart(2, "0")}`
  return h > 0 ? `${h}:${mmss}` : mmss
}

// 时间线的时间分隔线（参考微信）：今天只显示时刻，昨天、一周内带「昨天」或星期，
// 今年显示月日，更早带年份，例如「14:05」「昨天 14:05」「周三 14:05」「11月18日 14:05」
export function formatTimeDivider(ts: number, now: number): string {
  const diff = dayDiff(ts, now)
  const d = new Date(ts)
  const clock = formatClock(ts)
  if (diff <= 0) return clock
  if (diff === 1) return `昨天 ${clock}`
  if (diff < 7) return `${WEEKDAYS[d.getDay()]} ${clock}`
  const date = `${d.getMonth() + 1}月${d.getDate()}日 ${clock}`
  return d.getFullYear() === new Date(now).getFullYear() ? date : `${d.getFullYear()}年${date}`
}

// 禁言等未来时刻的截止时间：今天、明天只写日子和时刻，更晚写月日，不是今年时带年份，
// 例如「今天 18:30」「明天 09:00」「11月25日 14:00」
export function formatMuteEnd(ts: number, now: number): string {
  const ahead = -dayDiff(ts, now)
  const d = new Date(ts)
  const clock = formatClock(ts)
  if (ahead <= 0) return `今天 ${clock}`
  if (ahead === 1) return `明天 ${clock}`
  const date = `${d.getMonth() + 1}月${d.getDate()}日 ${clock}`
  return d.getFullYear() === new Date(now).getFullYear() ? date : `${d.getFullYear()}年${date}`
}

// 剩余时长的口语写法，用于禁言剩余时间：不足一小时按分钟（向上取整），不足一天按小时，否则按天
export function formatRemaining(ms: number): string {
  const minutes = Math.max(1, Math.ceil(ms / 60_000))
  if (minutes < 60) return `${minutes} 分钟`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours} 小时`
  return `${Math.floor(hours / 24)} 天`
}
