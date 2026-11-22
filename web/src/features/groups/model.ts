// 群的界面数据结构与纯函数，不依赖网络和浏览器，通知映射和单元测试可以直接引用。

import { GroupRole, GroupStatus, JoinMode, type Group as PbGroup } from "@/gen/social/v1/social_pb"
import type { ChatUser } from "@/features/chat/types"

export type Role = "owner" | "admin" | "member" | "none"
export type JoinPolicy = "free" | "approval"

export interface GroupInfo {
  id: string
  name: string
  // 非成员查询时为空
  notice: string
  ownerId: string
  joinMode: JoinPolicy
  muteAll: boolean
  memberCount: number
  dismissed: boolean
  // 修改资料时带上，不一致时服务端返回 GROUP_VERSION_CONFLICT
  version: number
  // 当前用户的角色；推送里的群资料不带这个字段
  myRole: Role
}

export interface MemberItem {
  user: ChatUser
  role: Role
  // 禁言截止时间（毫秒），未禁言或已过期时为空
  mutedUntil?: number
  joinedAt: number
}

export interface JoinRequestItem {
  id: string
  groupId: string
  user: ChatUser
  message: string
  createdAt: number
}

const ROLE: Record<GroupRole, Role> = {
  [GroupRole.UNSPECIFIED]: "none",
  [GroupRole.MEMBER]: "member",
  [GroupRole.ADMIN]: "admin",
  [GroupRole.OWNER]: "owner",
}

export const toRole = (r: GroupRole): Role => ROLE[r] ?? "none"

export const ROLE_LABEL: Record<Role, string> = { owner: "群主", admin: "管理员", member: "成员", none: "" }

export function toGroupInfo(g: PbGroup): GroupInfo {
  return {
    id: g.id.toString(),
    name: g.name || `群聊 ${g.id}`,
    notice: g.notice,
    ownerId: g.ownerId.toString(),
    joinMode: g.joinMode === JoinMode.APPROVAL ? "approval" : "free",
    muteAll: g.muteAll,
    memberCount: g.memberCount,
    dismissed: g.status === GroupStatus.DISMISSED,
    version: g.version,
    myRole: toRole(g.myRole),
  }
}

// 当前用户在群里能否发言（设计 3.6.4 的说明条），与 social 的 CheckSendPermission 同序：
// 解散、不是成员、单人禁言、全员禁言（群主和管理员不受限）
export function groupBlock(
  group: Pick<GroupInfo, "dismissed" | "myRole" | "muteAll">,
  mutedUntil: number | undefined,
  now: number,
): { reason: "GROUP_DISMISSED" | "NOT_GROUP_MEMBER" | "MEMBER_MUTED" | "GROUP_MUTED"; until?: number } | undefined {
  if (group.dismissed) return { reason: "GROUP_DISMISSED" }
  if (group.myRole === "none") return { reason: "NOT_GROUP_MEMBER" }
  if (mutedUntil !== undefined && mutedUntil > now) return { reason: "MEMBER_MUTED", until: mutedUntil }
  if (group.muteAll && group.myRole === "member") return { reason: "GROUP_MUTED" }
  return undefined
}

// 群主和管理员可以改资料、全员禁言、审批、邀请（social 的权限规则）
export const isManager = (role: Role) => role === "owner" || role === "admin"

// 能否移除、禁言某个成员：群主管理员和成员，管理员只管成员，不能操作自己
export const canManage = (me: Role, target: Role) => (me === "owner" && target !== "owner") || (me === "admin" && target === "member")

// 成员列表排序：群主、管理员、成员，同级按入群时间
const RANK: Record<Role, number> = { owner: 0, admin: 1, member: 2, none: 3 }
export function sortMembers(members: readonly MemberItem[]): MemberItem[] {
  return [...members].sort((a, b) => RANK[a.role] - RANK[b.role] || a.joinedAt - b.joinedAt)
}
