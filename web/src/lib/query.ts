// 好友、申请、黑名单、群资料与成员等「请求—响应」数据用 TanStack Query 缓存（设计第 5 节）。
// 推送只负责让对应的查询失效（realtime/notifications.ts），由查询重新拉取，不在推送里拼数据。

import { QueryClient } from "@tanstack/react-query"
import { isApiError } from "@/api/errors"

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // 变化靠通知帧驱动失效，不必频繁后台刷新；通知是尽力而为的，重连后整体失效一次兜底
      staleTime: 60_000,
      refetchOnWindowFocus: false,
      // 4xx（用户不存在、没有权限等）重试也没用，只对网络错误和 5xx 重试一次
      retry: (count, error) => count < 1 && !(isApiError(error) && error.status >= 400 && error.status < 500),
    },
    mutations: { retry: false },
  },
})

// 查询键集中在这里，失效映射和页面用同一份
export const queryKeys = {
  user: (userId: string) => ["user", userId] as const,
  friends: ["friends"] as const,
  receivedRequests: ["friend-requests", "received"] as const,
  sentRequests: ["friend-requests", "sent"] as const,
  blocks: ["blocks"] as const,
  groups: ["groups"] as const,
  group: (groupId: string) => ["group", groupId] as const,
  members: (groupId: string) => ["group", groupId, "members"] as const,
  joinRequests: (groupId: string) => ["group", groupId, "join-requests"] as const,
  sessions: ["sessions"] as const,
}

// 列表接口都是游标分页（cursor 为 0 表示第一页，next_cursor 为 0 表示没有下一页）。好友、群成员、
// 申请的数量都有上限，一次取完再在前端渲染；pages 防止服务端异常时无限循环
export async function fetchAll<T>(
  fetchPage: (cursor: bigint) => Promise<{ items: T[]; nextCursor: bigint }>,
  pages = 20,
): Promise<T[]> {
  const all: T[] = []
  let cursor = 0n
  for (let i = 0; i < pages; i++) {
    const { items, nextCursor } = await fetchPage(cursor)
    all.push(...items)
    if (nextCursor === 0n) break
    cursor = nextCursor
  }
  return all
}
