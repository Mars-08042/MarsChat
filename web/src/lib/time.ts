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

// 时间线的日期分隔线：今天、昨天、11月18日 周三；不是今年时带年份
export function formatDateDivider(ts: number, now: number): string {
  const diff = dayDiff(ts, now)
  const d = new Date(ts)
  if (diff <= 0) return "今天"
  if (diff === 1) return "昨天"
  const date = `${d.getMonth() + 1}月${d.getDate()}日 ${WEEKDAYS[d.getDay()]}`
  return d.getFullYear() === new Date(now).getFullYear() ? date : `${d.getFullYear()}年${date}`
}
