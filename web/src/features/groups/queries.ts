// 群资料、成员、入群申请的查询和操作。群聊会话 ID 即群 ID（data-model），两边可以直接互用。

import { timestampMs } from "@bufbuild/protobuf/wkt"
import { useQuery } from "@tanstack/react-query"
import { api } from "@/api/client"
import {
  acceptGroupJoinRequest as acceptJoinRequestEp,
  addGroupMembers as addGroupMembersEp,
  createGroup as createGroupEp,
  dismissGroup as dismissGroupEp,
  getGroup,
  joinGroup as joinGroupEp,
  leaveGroup as leaveGroupEp,
  listGroupJoinRequests,
  listGroupMembers,
  listMyGroups,
  muteGroupMember as muteGroupMemberEp,
  rejectGroupJoinRequest as rejectJoinRequestEp,
  removeGroupMember as removeGroupMemberEp,
  setGroupMemberRole as setGroupMemberRoleEp,
  transferGroupOwner as transferGroupOwnerEp,
  updateGroup as updateGroupEp,
} from "@/api/endpoints"
import type { UserProfile } from "@/gen/account/v1/account_pb"
import { GroupRole, JoinMode, type GroupJoinRequest as PbJoinRequest, type GroupMember as PbMember } from "@/gen/social/v1/social_pb"
import type { ChatUser } from "@/features/chat/types"
import { fetchAll, queryClient, queryKeys } from "@/lib/query"
import { toChatUser } from "@/realtime/convert"
import { chatStore } from "@/stores/chat"
import { toGroupInfo, toRole, type GroupInfo, type JoinPolicy, type JoinRequestItem, type MemberItem } from "./model"

const PAGE_SIZE = 100

function profileOf(userId: bigint, profile: UserProfile | undefined): ChatUser {
  return profile ? toChatUser(profile) : { id: userId.toString(), nickname: `用户 ${userId}` }
}

function toMember(m: PbMember): MemberItem {
  const mutedUntil = m.mutedUntil ? timestampMs(m.mutedUntil) : undefined
  return {
    user: profileOf(m.userId, m.profile),
    role: toRole(m.role),
    mutedUntil,
    joinedAt: m.joinedAt ? timestampMs(m.joinedAt) : 0,
  }
}

function toJoinRequest(r: PbJoinRequest): JoinRequestItem {
  return {
    id: r.id.toString(),
    groupId: r.groupId.toString(),
    user: profileOf(r.userId, r.profile),
    message: r.message,
    createdAt: r.createdAt ? timestampMs(r.createdAt) : 0,
  }
}

async function fetchGroups(): Promise<GroupInfo[]> {
  const list = await fetchAll(async (cursor) => {
    const res = await api.send(listMyGroups, { cursor, limit: PAGE_SIZE })
    return { items: res.groups, nextCursor: res.nextCursor }
  })
  return list.map(toGroupInfo)
}

export async function fetchGroup(groupId: string): Promise<GroupInfo> {
  const { group } = await api.send(getGroup, { groupId: BigInt(groupId) })
  if (!group) throw new Error("get group response without group")
  return toGroupInfo(group)
}

// 成员资料顺带写入聊天 store，群聊里的发送者昵称随之更新
async function fetchMembers(groupId: string): Promise<MemberItem[]> {
  const list = await fetchAll(async (cursor) => {
    const res = await api.send(listGroupMembers, { groupId: BigInt(groupId), cursor, limit: PAGE_SIZE })
    return { items: res.members, nextCursor: res.nextCursor }
  })
  const members = list.map(toMember)
  chatStore.setState((s) => {
    const users = { ...s.users }
    for (const m of members) users[m.user.id] = m.user
    return { users }
  })
  return members
}

async function fetchJoinRequests(groupId: string): Promise<JoinRequestItem[]> {
  const list = await fetchAll(async (cursor) => {
    const res = await api.send(listGroupJoinRequests, { groupId: BigInt(groupId), cursor, limit: PAGE_SIZE })
    return { items: res.requests, nextCursor: res.nextCursor }
  })
  return list.map(toJoinRequest)
}

export const groupQuery = (groupId: string) => ({ queryKey: queryKeys.group(groupId), queryFn: () => fetchGroup(groupId) })
export const membersQuery = (groupId: string) => ({ queryKey: queryKeys.members(groupId), queryFn: () => fetchMembers(groupId) })

export const useGroups = () => useQuery({ queryKey: queryKeys.groups, queryFn: fetchGroups })
export const useGroup = (groupId: string, enabled = true) => useQuery({ ...groupQuery(groupId), enabled })
export const useMembers = (groupId: string, enabled = true) => useQuery({ ...membersQuery(groupId), enabled })
// 只有群主和管理员能查，只返回待处理的申请
export const useJoinRequests = (groupId: string, enabled = true) =>
  useQuery({ queryKey: queryKeys.joinRequests(groupId), queryFn: () => fetchJoinRequests(groupId), enabled })

const invalidate = (...keys: ReadonlyArray<readonly unknown[]>) =>
  Promise.all(keys.map((queryKey) => queryClient.invalidateQueries({ queryKey })))

// 群资料、成员、申请都以 ["group", id] 开头，一次失效
const invalidateGroup = (groupId: string) => invalidate(queryKeys.groups, ["group", groupId])

export interface NewGroup {
  name: string
  notice: string
  joinMode: JoinPolicy
  memberIds: string[]
}

export async function createGroup(g: NewGroup): Promise<GroupInfo> {
  const { group } = await api.send(createGroupEp, {
    name: g.name,
    notice: g.notice,
    joinMode: g.joinMode === "approval" ? JoinMode.APPROVAL : JoinMode.FREE,
    memberIds: g.memberIds.map(BigInt),
  })
  if (!group) throw new Error("create group response without group")
  await invalidate(queryKeys.groups)
  return toGroupInfo(group)
}

export interface GroupPatch {
  name?: string
  notice?: string
  joinMode?: JoinPolicy
  muteAll?: boolean
  // 头像文件 ID，空字符串表示移除
  avatar?: string
}

// 只发送变化的字段；version 是读到的资料版本，被别人改过时返回 GROUP_VERSION_CONFLICT
export async function updateGroup(groupId: string, version: number, patch: GroupPatch): Promise<GroupInfo> {
  try {
    const { group } = await api.send(updateGroupEp, {
      groupId: BigInt(groupId),
      version,
      name: patch.name,
      notice: patch.notice,
      joinMode: patch.joinMode === undefined ? undefined : patch.joinMode === "approval" ? JoinMode.APPROVAL : JoinMode.FREE,
      muteAll: patch.muteAll,
      avatar: patch.avatar,
    })
    if (!group) throw new Error("update group response without group")
    const info = toGroupInfo(group)
    queryClient.setQueryData(queryKeys.group(groupId), info)
    await invalidate(queryKeys.groups)
    return info
  } catch (e) {
    // 版本冲突时重新读取，界面显示最新资料
    await invalidateGroup(groupId)
    throw e
  }
}

// 直接加入的群返回 joined；需审核的群返回待处理的申请
export async function joinGroup(groupId: string, message: string): Promise<{ joined: boolean }> {
  const res = await api.send(joinGroupEp, { groupId: BigInt(groupId), message })
  await invalidateGroup(groupId)
  return { joined: res.joined }
}

export async function acceptJoinRequest(groupId: string, requestId: string) {
  try {
    await api.send(acceptJoinRequestEp, { requestId: BigInt(requestId) })
  } finally {
    // 已被其他管理员处理时也刷新列表，把这条申请去掉
    await invalidateGroup(groupId)
  }
}

export async function rejectJoinRequest(groupId: string, requestId: string) {
  try {
    await api.send(rejectJoinRequestEp, { requestId: BigInt(requestId) })
  } finally {
    await invalidate(queryKeys.joinRequests(groupId))
  }
}

// 返回实际加入的人，不含原本已是成员的人
export async function addMembers(groupId: string, userIds: string[]): Promise<string[]> {
  const res = await api.send(addGroupMembersEp, { groupId: BigInt(groupId), userIds: userIds.map(BigInt) })
  await invalidateGroup(groupId)
  return res.addedUserIds.map(String)
}

export async function removeMember(groupId: string, userId: string) {
  await api.send(removeGroupMemberEp, { groupId: BigInt(groupId), userId: BigInt(userId) })
  await invalidateGroup(groupId)
}

export async function setRole(groupId: string, userId: string, role: "admin" | "member") {
  await api.send(setGroupMemberRoleEp, {
    groupId: BigInt(groupId),
    userId: BigInt(userId),
    role: role === "admin" ? GroupRole.ADMIN : GroupRole.MEMBER,
  })
  await invalidate(queryKeys.members(groupId))
}

// seconds 为 0 时解除禁言，最长 30 天
export async function muteMember(groupId: string, userId: string, seconds: number) {
  await api.send(muteGroupMemberEp, { groupId: BigInt(groupId), userId: BigInt(userId), durationSeconds: seconds })
  await invalidate(queryKeys.members(groupId))
}

export async function transferOwner(groupId: string, newOwnerId: string) {
  await api.send(transferGroupOwnerEp, { groupId: BigInt(groupId), newOwnerId: BigInt(newOwnerId) })
  await invalidateGroup(groupId)
}

export async function leaveGroup(groupId: string) {
  await api.send(leaveGroupEp, { groupId: BigInt(groupId) })
  await invalidateGroup(groupId)
}

export async function dismissGroup(groupId: string) {
  await api.send(dismissGroupEp, { groupId: BigInt(groupId) })
  await invalidateGroup(groupId)
}
