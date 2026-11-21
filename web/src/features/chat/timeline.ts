// 把一个会话的消息排成时间线条目：日期分隔线、「以下为新消息」分隔线、分组（设计 3.6.3）。

import { isSameDay } from "@/lib/time"

// 同一发送者间隔不超过 5 分钟的连续消息合并为一组
export const GROUP_WINDOW_MS = 5 * 60 * 1000

export interface TimelineMessage {
  id: string
  senderId: string
  sentAt: number
}

export type TimelineItem<M extends TimelineMessage> =
  | { kind: "date"; key: string; at: number }
  | { kind: "unread"; key: string }
  | { kind: "message"; key: string; message: M; firstInGroup: boolean; lastInGroup: boolean }

// messages 按时间升序；firstUnreadId 是打开会话时第一条未读消息，没有未读时不传
export function buildTimeline<M extends TimelineMessage>(
  messages: readonly M[],
  firstUnreadId?: string,
): TimelineItem<M>[] {
  // 先算出每条消息是否开启新组：跨天、未读分隔线、换了发送者、间隔超过窗口，任一成立都要断开
  const startsGroup = messages.map((m, i) => {
    if (i === 0) return true
    const prev = messages[i - 1]
    return (
      !isSameDay(prev.sentAt, m.sentAt) ||
      m.id === firstUnreadId ||
      prev.senderId !== m.senderId ||
      m.sentAt - prev.sentAt > GROUP_WINDOW_MS
    )
  })

  const items: TimelineItem<M>[] = []
  messages.forEach((m, i) => {
    if (i === 0 || !isSameDay(messages[i - 1].sentAt, m.sentAt)) {
      items.push({ kind: "date", key: `date-${m.id}`, at: m.sentAt })
    }
    if (m.id === firstUnreadId) {
      items.push({ kind: "unread", key: "unread" })
    }
    items.push({
      kind: "message",
      key: m.id,
      message: m,
      firstInGroup: startsGroup[i],
      lastInGroup: i === messages.length - 1 || startsGroup[i + 1],
    })
  })
  return items
}
