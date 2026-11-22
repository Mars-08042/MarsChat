// 重连等待时间（设计 3.9.2）：连续失败按 1、2、4、8 秒……指数增长，最长 30 秒，每次再加 0～2 秒随机抖动，
// 避免大量客户端在同一时刻重连（data-flow 第 7 节）。认证成功后失败计数清零。

export const RECONNECT_MAX_MS = 30_000
export const RECONNECT_JITTER_MS = 2_000

// failures 是这次重连之前已经连续失败的次数。0 表示刚才还在线（服务停机 1001、连接异常断开等），只等随机抖动
export function reconnectDelay(failures: number, random: () => number = Math.random): number {
  const base = failures <= 0 ? 0 : Math.min(1000 * 2 ** (failures - 1), RECONNECT_MAX_MS)
  return base + Math.floor(random() * RECONNECT_JITTER_MS)
}
