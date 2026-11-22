import { describe, expect, it } from "vitest"
import { formatClock, formatDuration, formatListTime, formatMuteEnd, formatRemaining, formatTimeDivider, isSameDay } from "./time"

// 用本地时间构造，避免测试结果依赖运行环境的时区
const at = (y: number, mo: number, d: number, h = 12, mi = 0) => new Date(y, mo - 1, d, h, mi).getTime()
const NOW = at(2026, 11, 21, 15, 30) // 周六

describe("formatClock", () => {
  it("补齐两位", () => {
    expect(formatClock(at(2026, 11, 21, 9, 5))).toBe("09:05")
    expect(formatClock(at(2026, 11, 21, 23, 59))).toBe("23:59")
  })
})

describe("isSameDay", () => {
  it("按本地日期判断", () => {
    expect(isSameDay(at(2026, 11, 21, 0, 0), at(2026, 11, 21, 23, 59))).toBe(true)
    expect(isSameDay(at(2026, 11, 20, 23, 59), at(2026, 11, 21, 0, 0))).toBe(false)
  })
})

describe("formatListTime", () => {
  it.each([
    [at(2026, 11, 21, 8, 1), "08:01"],
    [at(2026, 11, 20, 23, 0), "昨天"],
    [at(2026, 11, 18), "周三"],
    [at(2026, 11, 15), "周日"],
    [at(2026, 11, 14), "11/14"],
    [at(2026, 1, 3), "1/3"],
    [at(2025, 12, 31), "2025/12/31"],
  ])("%s → %s", (ts, want) => {
    expect(formatListTime(ts, NOW)).toBe(want)
  })

  it("本机时间略慢于消息时间时仍显示时刻", () => {
    expect(formatListTime(at(2026, 11, 22, 0, 1), NOW)).toBe("00:01")
  })
})

describe("formatDuration", () => {
  it.each([
    [0, "00:00"],
    [999, "00:00"],
    [61_000, "01:01"],
    [59 * 60_000 + 59_000, "59:59"],
    [3600_000 + 3 * 60_000 + 21_000, "1:03:21"],
    [-5_000, "00:00"],
  ])("%d ms → %s", (ms, want) => {
    expect(formatDuration(ms)).toBe(want)
  })
})

describe("formatTimeDivider", () => {
  it.each([
    [at(2026, 11, 21, 9, 5), "09:05"],
    [at(2026, 11, 20, 18, 30), "昨天 18:30"],
    [at(2026, 11, 18, 8, 0), "周三 08:00"],
    [at(2026, 11, 14, 12, 0), "11月14日 12:00"],
    [at(2025, 11, 18, 20, 15), "2025年11月18日 20:15"],
  ])("%s → %s", (ts, want) => {
    expect(formatTimeDivider(ts, NOW)).toBe(want)
  })
})

describe("formatMuteEnd", () => {
  it.each([
    [at(2026, 11, 21, 18, 30), "今天 18:30"],
    [at(2026, 11, 22, 9, 0), "明天 09:00"],
    [at(2026, 11, 25, 14, 0), "11月25日 14:00"],
    [at(2027, 1, 2, 8, 0), "2027年1月2日 08:00"],
  ])("%s → %s", (ts, want) => {
    expect(formatMuteEnd(ts, NOW)).toBe(want)
  })
})

describe("formatRemaining", () => {
  it.each([
    [30_000, "1 分钟"],
    [10 * 60_000 + 1, "11 分钟"],
    [2 * 3_600_000, "2 小时"],
    [3 * 86_400_000 + 5, "3 天"],
  ])("%s → %s", (ms, want) => {
    expect(formatRemaining(ms)).toBe(want)
  })
})
