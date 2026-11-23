import { UserStatus } from "@/gen/account/v1/account_pb"

export const PAGE_SIZE = 20

// 状态筛选放在地址的查询串里（?status=normal），刷新、后退保持
export type StatusFilter = "all" | "normal" | "banned"

export const STATUS_FILTERS: readonly { id: StatusFilter; label: string }[] = [
  { id: "all", label: "全部" },
  { id: "normal", label: "正常" },
  { id: "banned", label: "已封禁" },
]

export function parseStatusFilter(value: string | null): StatusFilter {
  return value === "normal" || value === "banned" ? value : "all"
}

export function statusOf(filter: StatusFilter): UserStatus {
  if (filter === "normal") return UserStatus.NORMAL
  if (filter === "banned") return UserStatus.BANNED
  return UserStatus.UNSPECIFIED
}

// 总页数至少为 1；封禁、解封后总数变小时，当前页可能超过它，显示时取两者较大的
export function pageCount(total: number, size = PAGE_SIZE): number {
  return Math.max(1, Math.ceil(total / size))
}
