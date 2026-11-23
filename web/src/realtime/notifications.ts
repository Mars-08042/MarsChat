// 好友与群通知（ServerFrame.notification）到界面效果的映射（设计 3.6.7）。
// 只算「该做什么」：让哪些查询失效、弹什么提示、哪个会话要刷新或移除；执行在 client.ts。
// 通知是尽力而为的，断线期间的不补发，重连后由列表接口兜底，所以这里不维护任何本地数据。

import { timestampMs } from "@bufbuild/protobuf/wkt"
import type { Notification } from "@/gen/access/v1/frame_pb"
import { LeaveReason } from "@/gen/social/v1/events_pb"
import { toRole, type Role } from "@/features/groups/model"
import { queryKeys } from "@/lib/query"
import { formatMuteEnd } from "@/lib/time"

export interface ToastAction {
  label: string
  to: string
  // 打开会话后展开群面板的哪一页
  panel?: "requests"
}

export interface NotificationEffects {
  invalidate: ReadonlyArray<readonly unknown[]>
  toast?: { title: string; description?: string; action?: ToastAction }
  // 播放庆祝动效（成为好友）
  celebrate?: boolean
  // 群资料、成员数或解散状态变化，或者自己刚进群：重新获取这个会话
  refreshConversation?: string
  // 自己退群或被移出：会话从列表中移除
  removeConversation?: string
  // 自己在这个群里的发言权限可能变了，重新计算说明条
  recheckGroup?: string
  // 和这个用户成为好友或不再是好友，单聊的「不是好友」说明条随之出现或消失
  friendship?: { userId: string; friends: boolean }
  // 通知里带出的自己在群里的最新角色和禁言截止时间，供下一条成员变更通知对比
  selfMember?: { groupId: string } & SelfMember
}

export interface SelfMember {
  role: Role
  mutedUntil?: number
}

export interface NotificationContext {
  meId: string
  userName(userId: string): string | undefined
  groupName(groupId: string): string | undefined
  // 通知到达前自己在群里的角色和禁言截止时间，不知道时为空
  previousSelf(groupId: string): SelfMember | undefined
  now: number
}

const quote = (name: string | undefined) => (name ? `「${name}」` : "")

export function notificationEffects(n: Notification, ctx: NotificationContext): NotificationEffects {
  const { meId } = ctx
  const body = n.body
  switch (body.case) {
    case "friendRequestCreated": {
      const v = body.value
      const from = v.fromUserId.toString()
      if (from === meId) return { invalidate: [queryKeys.sentRequests] }
      const name = ctx.userName(from)
      return {
        invalidate: [queryKeys.receivedRequests],
        toast: {
          title: name ? `${name} 请求添加你为好友` : "收到新的好友申请",
          description: v.message || undefined,
          action: { label: "查看", to: "/contacts/requests" },
        },
      }
    }

    case "friendAdded": {
      const v = body.value
      // user_id 是同意申请的一方；自己同意时界面已经播放过动效，这里不再提示
      const other = v.userId.toString() === meId ? v.friendId.toString() : v.userId.toString()
      const effects: NotificationEffects = {
        invalidate: [queryKeys.friends, queryKeys.receivedRequests, queryKeys.sentRequests],
        friendship: { userId: other, friends: true },
      }
      if (v.userId.toString() === meId) return effects
      const name = ctx.userName(other) ?? `用户 ${other}`
      return {
        ...effects,
        toast: { title: `你和 ${name} 已成为好友`, action: { label: "发消息", to: `/chat/with/${other}` } },
        celebrate: true,
      }
    }

    case "friendDeleted": {
      const v = body.value
      const other = v.userId.toString() === meId ? v.friendId.toString() : v.userId.toString()
      // 删除好友不提示（设计 3.6.7）
      return { invalidate: [queryKeys.friends], friendship: { userId: other, friends: false } }
    }

    case "groupCreated": {
      const g = body.value.group
      if (!g) return { invalidate: [queryKeys.groups] }
      const id = g.id.toString()
      const owner = g.ownerId.toString() === meId
      const effects: NotificationEffects = {
        invalidate: [queryKeys.groups],
        refreshConversation: id,
        selfMember: { groupId: id, role: owner ? "owner" : "member" },
      }
      // 自己建的群由建群向导提示
      if (owner) return effects
      return { ...effects, toast: { title: `你已加入群${quote(g.name)}`, action: { label: "查看", to: `/chat/${id}` } } }
    }

    case "groupMembersJoined": {
      const v = body.value
      const id = v.groupId.toString()
      const effects: NotificationEffects = { invalidate: [queryKeys.groups, ["group", id]], refreshConversation: id }
      const meJoined = v.userIds.some((u) => u.toString() === meId)
      if (!meJoined) return effects
      const withAccess: NotificationEffects = { ...effects, recheckGroup: id, selfMember: { groupId: id, role: "member" } }
      // 自己直接加入的群由加群对话框提示；被邀请、申请被同意时提示
      if (v.operatorId.toString() === meId) return withAccess
      return { ...withAccess, toast: { title: `你已加入群${quote(ctx.groupName(id))}`, action: { label: "查看", to: `/chat/${id}` } } }
    }

    case "groupMemberLeft": {
      const v = body.value
      const id = v.groupId.toString()
      const invalidate = [queryKeys.groups, ["group", id]]
      if (v.userId.toString() !== meId) return { invalidate, refreshConversation: id }
      const effects: NotificationEffects = { invalidate, removeConversation: id }
      if (v.reason !== LeaveReason.REMOVED) return effects
      return { ...effects, toast: { title: `你已被移出群${quote(ctx.groupName(id))}` } }
    }

    case "groupMemberUpdated": {
      const v = body.value
      const id = v.groupId.toString()
      const invalidate = [["group", id]]
      if (v.userId.toString() !== meId) return { invalidate }
      const next: SelfMember = { role: toRole(v.role), mutedUntil: v.mutedUntil ? timestampMs(v.mutedUntil) : undefined }
      const effects: NotificationEffects = { invalidate, recheckGroup: id, selfMember: { groupId: id, ...next } }
      if (v.operatorId.toString() === meId) return effects
      const toast = memberUpdateToast(ctx.groupName(id), ctx.previousSelf(id), next.role, next.mutedUntil, ctx.now)
      return toast ? { ...effects, toast: { title: toast } } : effects
    }

    case "groupUpdated": {
      const g = body.value.group
      if (!g) return { invalidate: [queryKeys.groups] }
      const id = g.id.toString()
      // 全员禁言可能开关了，重新计算说明条；推送里的群资料不带 my_role，不能直接用
      return { invalidate: [queryKeys.groups, ["group", id]], refreshConversation: id, recheckGroup: id }
    }

    case "groupDismissed": {
      const v = body.value
      const id = v.groupId.toString()
      // 会话保留，只能看历史；会话信息里的群状态变为已解散，说明条随之出现
      const effects: NotificationEffects = { invalidate: [queryKeys.groups, ["group", id]], refreshConversation: id }
      if (v.operatorId.toString() === meId) return effects
      return { ...effects, toast: { title: `群${quote(ctx.groupName(id))}已解散` } }
    }

    case "groupJoinRequestCreated": {
      const v = body.value
      const id = v.groupId.toString()
      const user = ctx.userName(v.userId.toString()) ?? `用户 ${v.userId}`
      return {
        invalidate: [queryKeys.joinRequests(id)],
        toast: {
          title: `${user} 申请加入群${quote(ctx.groupName(id))}`,
          description: v.message || undefined,
          action: { label: "处理", to: `/chat/${id}`, panel: "requests" },
        },
      }
    }

    default:
      // 新版本服务端增加的通知类型，旧客户端忽略
      return { invalidate: [] }
  }
}

// 这条通知的提示里要用到哪些用户名和群名。本地没有时 client.ts 先去查，查不到再用通用文案
export function namesNeeded(n: Notification, meId: string): { users: string[]; groups: string[] } {
  const none = { users: [], groups: [] }
  const body = n.body
  switch (body.case) {
    case "friendRequestCreated": {
      const from = body.value.fromUserId.toString()
      return from === meId ? none : { users: [from], groups: [] }
    }
    case "friendAdded": {
      const v = body.value
      return v.userId.toString() === meId ? none : { users: [v.userId.toString()], groups: [] }
    }
    case "groupMembersJoined": {
      const v = body.value
      const toasted = v.operatorId.toString() !== meId && v.userIds.some((u) => u.toString() === meId)
      return toasted ? { users: [], groups: [v.groupId.toString()] } : none
    }
    case "groupMemberLeft": {
      const v = body.value
      const toasted = v.userId.toString() === meId && v.reason === LeaveReason.REMOVED
      return toasted ? { users: [], groups: [v.groupId.toString()] } : none
    }
    case "groupMemberUpdated":
    case "groupDismissed": {
      const v = body.value
      const self = body.case === "groupDismissed" || body.value.userId.toString() === meId
      return self && v.operatorId.toString() !== meId ? { users: [], groups: [v.groupId.toString()] } : none
    }
    case "groupJoinRequestCreated":
      return { users: [body.value.userId.toString()], groups: [body.value.groupId.toString()] }
    default:
      return none
  }
}

// 自己的角色或禁言被别人修改时的提示。转让群主时新旧群主各收到一条。
// 不知道修改前的状态（本次登录还没打开过这个群，也没收到过它的成员通知）时只能判断「现在被禁言」，
// 其余变化不提示，避免误报：例如管理员被禁言时推送里的角色也是管理员
function memberUpdateToast(
  groupName: string | undefined,
  prev: SelfMember | undefined,
  role: Role,
  mutedUntil: number | undefined,
  now: number,
): string | undefined {
  const group = `群${quote(groupName)}`
  if (prev && prev.role !== role) {
    if (role === "owner") return `你已成为${group}的群主`
    if (role === "admin") return `你已成为${group}的管理员`
    if (prev.role === "admin") return `你在${group}的管理员身份已被取消`
  }
  const muted = mutedUntil !== undefined && mutedUntil > now
  const wasMuted = prev?.mutedUntil !== undefined && prev.mutedUntil > now
  if (muted && (!prev || prev.mutedUntil !== mutedUntil)) return `你在${group}被禁言，${formatMuteEnd(mutedUntil, now)} 解除`
  if (!muted && wasMuted) return `你在${group}的禁言已解除`
  return undefined
}

// 按 event_id 去重：同一事件可能经多条路径重复推送。只记最近的 capacity 个
export function createDeduper(capacity = 1000): (eventId: string) => boolean {
  const seen = new Set<string>()
  return (eventId) => {
    if (!eventId) return true
    if (seen.has(eventId)) return false
    seen.add(eventId)
    if (seen.size > capacity) {
      const oldest = seen.values().next().value
      if (oldest !== undefined) seen.delete(oldest)
    }
    return true
  }
}
