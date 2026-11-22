import { describe, expect, it } from "vitest"
import { buildTimeline, GROUP_WINDOW_MS, TIME_GAP_MS, type TimelineItem, type TimelineMessage } from "./timeline"

const base = new Date(2026, 10, 21, 10, 0).getTime()
const MIN = 60_000

const msg = (id: string, senderId: string, sentAt: number): TimelineMessage => ({ id, senderId, sentAt })

// 把条目压缩成便于断言的字符串：time、unread、消息 id 加分组首尾标记
function shape(items: TimelineItem<TimelineMessage>[]): string[] {
  return items.map((it) => {
    if (it.kind !== "message") return it.kind
    return `${it.message.id}${it.firstInGroup ? "^" : ""}${it.lastInGroup ? "$" : ""}`
  })
}

describe("buildTimeline", () => {
  it("空列表没有任何条目", () => {
    expect(buildTimeline([])).toEqual([])
  })

  it("第一条消息前有时间分隔线，自成一组", () => {
    expect(shape(buildTimeline([msg("a", "u1", base)]))).toEqual(["time", "a^$"])
  })

  it("同一发送者 5 分钟内的连续消息合并为一组，中间不显示时间", () => {
    const items = buildTimeline([
      msg("a", "u1", base),
      msg("b", "u1", base + 2 * MIN),
      msg("c", "u1", base + 2 * MIN + GROUP_WINDOW_MS),
    ])
    expect(shape(items)).toEqual(["time", "a^", "b", "c$"])
  })

  it("不同发送者间隔不到 5 分钟时只断开分组，不显示时间", () => {
    const items = buildTimeline([msg("a", "u1", base), msg("b", "u2", base + MIN), msg("c", "u1", base + 2 * MIN)])
    expect(shape(items)).toEqual(["time", "a^$", "b^$", "c^$"])
  })

  it("间隔超过 5 分钟时插入时间分隔线并断开分组", () => {
    const items = buildTimeline([msg("a", "u1", base), msg("b", "u1", base + TIME_GAP_MS + 1)])
    expect(shape(items)).toEqual(["time", "a^$", "time", "b^$"])
    expect(items[2]).toMatchObject({ kind: "time", at: base + TIME_GAP_MS + 1 })
  })

  it("跨天时插入时间分隔线，即使间隔不到 5 分钟", () => {
    const night = new Date(2026, 10, 21, 23, 58).getTime()
    const items = buildTimeline([msg("a", "u1", night), msg("b", "u1", night + 3 * MIN)])
    expect(shape(items)).toEqual(["time", "a^$", "time", "b^$"])
  })

  it("未读分隔线插在第一条未读消息前，并断开分组", () => {
    const items = buildTimeline(
      [msg("a", "u1", base), msg("b", "u1", base + MIN), msg("c", "u1", base + 2 * MIN)],
      "b",
    )
    expect(shape(items)).toEqual(["time", "a^$", "unread", "b^", "c$"])
  })

  it("第一条未读消息前也要显示时间时，时间分隔线在未读分隔线之前", () => {
    const items = buildTimeline([msg("a", "u1", base - 86_400_000), msg("b", "u2", base)], "b")
    expect(shape(items)).toEqual(["time", "a^$", "time", "unread", "b^$"])
  })

  it("找不到 firstUnreadId 时不插入未读分隔线", () => {
    expect(shape(buildTimeline([msg("a", "u1", base)], "missing"))).toEqual(["time", "a^$"])
  })

  it("条目的 key 唯一", () => {
    const items = buildTimeline(
      [msg("a", "u1", base - 86_400_000), msg("b", "u1", base), msg("c", "u2", base + MIN)],
      "c",
    )
    const keys = items.map((it) => it.key)
    expect(new Set(keys).size).toBe(keys.length)
  })
})
