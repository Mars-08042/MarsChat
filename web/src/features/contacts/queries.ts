// 好友、好友申请、黑名单与用户资料的查询和操作。操作成功后让相关查询失效；
// 同一操作的通知帧随后到达时还会再失效一次，重复拉取的代价可以接受。

import { timestampMs } from "@bufbuild/protobuf/wkt"
import { useQuery } from "@tanstack/react-query"
import { api } from "@/api/client"
import {
  acceptFriendRequest as acceptFriendRequestEp,
  blockUser as blockUserEp,
  deleteFriend as deleteFriendEp,
  getUser,
  listBlockedUsers,
  listFriends,
  listReceivedFriendRequests,
  listSentFriendRequests,
  rejectFriendRequest as rejectFriendRequestEp,
  sendFriendRequest as sendFriendRequestEp,
  unblockUser as unblockUserEp,
} from "@/api/endpoints"
import type { UserProfile } from "@/gen/account/v1/account_pb"
import { RequestStatus, type FriendRequest as PbFriendRequest } from "@/gen/social/v1/social_pb"
import type { ChatUser } from "@/features/chat/types"
import { fetchAll, queryClient, queryKeys } from "@/lib/query"
import { toChatUser } from "@/realtime/convert"
import { chatStore } from "@/stores/chat"

const PAGE_SIZE = 100

export type RequestState = "pending" | "accepted" | "rejected" | "expired"

export interface FriendItem {
  user: ChatUser
  // 成为好友的时间
  since: number
}

export interface FriendRequestItem {
  id: string
  // 收到的申请是申请人，发出的申请是接收方
  peer: ChatUser
  message: string
  state: RequestState
  // 重复申请会刷新附言和这个时间
  updatedAt: number
}

export interface BlockedItem {
  user: ChatUser
  since: number
}

const STATE: Record<RequestStatus, RequestState> = {
  [RequestStatus.UNSPECIFIED]: "pending",
  [RequestStatus.PENDING]: "pending",
  [RequestStatus.ACCEPTED]: "accepted",
  [RequestStatus.REJECTED]: "rejected",
  [RequestStatus.EXPIRED]: "expired",
}

// 资料缺失时（account 降级返回空资料）用 ID 兜底，名称显示「用户 <ID>」
function profileOf(userId: bigint, profile: UserProfile | undefined): ChatUser {
  return profile ? toChatUser(profile) : { id: userId.toString(), nickname: `用户 ${userId}` }
}

// 查询拿到的资料顺带写入聊天 store，会话标题和消息发送者的昵称随之更新
function rememberUsers(users: ChatUser[]) {
  if (users.length === 0) return
  chatStore.setState((s) => {
    const next = { ...s.users }
    for (const u of users) next[u.id] = u
    return { users: next }
  })
}

function toRequest(r: PbFriendRequest, peerId: bigint): FriendRequestItem {
  return {
    id: r.id.toString(),
    peer: profileOf(peerId, r.peer),
    message: r.message,
    state: STATE[r.status] ?? "pending",
    updatedAt: r.updatedAt ? timestampMs(r.updatedAt) : r.createdAt ? timestampMs(r.createdAt) : 0,
  }
}

async function fetchFriends(): Promise<FriendItem[]> {
  const list = await fetchAll(async (cursor) => {
    const res = await api.send(listFriends, { cursor, limit: PAGE_SIZE })
    return { items: res.friends, nextCursor: res.nextCursor }
  })
  const items = list.map((f) => ({ user: profileOf(f.userId, f.profile), since: f.createdAt ? timestampMs(f.createdAt) : 0 }))
  rememberUsers(items.map((f) => f.user))
  return items
}

async function fetchReceived(): Promise<FriendRequestItem[]> {
  const list = await fetchAll(async (cursor) => {
    const res = await api.send(listReceivedFriendRequests, { cursor, limit: PAGE_SIZE })
    return { items: res.requests, nextCursor: res.nextCursor }
  })
  return list.map((r) => toRequest(r, r.fromUserId))
}

async function fetchSent(): Promise<FriendRequestItem[]> {
  const list = await fetchAll(async (cursor) => {
    const res = await api.send(listSentFriendRequests, { cursor, limit: PAGE_SIZE })
    return { items: res.requests, nextCursor: res.nextCursor }
  })
  return list.map((r) => toRequest(r, r.toUserId))
}

async function fetchBlocks(): Promise<BlockedItem[]> {
  const list = await fetchAll(async (cursor) => {
    const res = await api.send(listBlockedUsers, { cursor, limit: PAGE_SIZE })
    return { items: res.users, nextCursor: res.nextCursor }
  })
  return list.map((b) => ({ user: profileOf(b.userId, b.profile), since: b.createdAt ? timestampMs(b.createdAt) : 0 }))
}

export const useFriends = () => useQuery({ queryKey: queryKeys.friends, queryFn: fetchFriends })
// 收到的申请只返回待处理的；发出的申请返回全部，能看到对方是否已处理
export const useReceivedRequests = () => useQuery({ queryKey: queryKeys.receivedRequests, queryFn: fetchReceived })
export const useSentRequests = () => useQuery({ queryKey: queryKeys.sentRequests, queryFn: fetchSent })
export const useBlocks = () => useQuery({ queryKey: queryKeys.blocks, queryFn: fetchBlocks })

// 资料卡打开时取一次最新资料；之前已在 store 里的资料先显示出来
export function useUserProfile(userId: string, enabled: boolean) {
  return useQuery({
    queryKey: queryKeys.user(userId),
    queryFn: async () => {
      const { user } = await api.send(getUser, { userId: BigInt(userId) })
      const u = profileOf(BigInt(userId), user)
      rememberUsers([u])
      return u
    },
    enabled,
    staleTime: 30_000,
    placeholderData: () => chatStore.getState().users[userId],
  })
}

const invalidate = (...keys: ReadonlyArray<readonly unknown[]>) =>
  Promise.all(keys.map((queryKey) => queryClient.invalidateQueries({ queryKey })))

// 对方已经向我发过申请时，服务端直接让双方成为好友，became 为 true
export async function sendFriendRequest(userId: string, message: string): Promise<{ became: boolean }> {
  const res = await api.send(sendFriendRequestEp, { userId: BigInt(userId), message })
  await invalidate(queryKeys.sentRequests, ...(res.becameFriends ? [queryKeys.friends, queryKeys.receivedRequests] : []))
  return { became: res.becameFriends }
}

export async function acceptFriendRequest(requestId: string) {
  await api.send(acceptFriendRequestEp, { requestId: BigInt(requestId) })
  await invalidate(queryKeys.receivedRequests, queryKeys.friends)
}

export async function rejectFriendRequest(requestId: string) {
  await api.send(rejectFriendRequestEp, { requestId: BigInt(requestId) })
  await invalidate(queryKeys.receivedRequests)
}

export async function deleteFriend(userId: string) {
  await api.send(deleteFriendEp, { userId: BigInt(userId) })
  await invalidate(queryKeys.friends)
}

// 拉黑不删除好友关系，两人之间待处理的申请随之失效
export async function blockUser(userId: string) {
  await api.send(blockUserEp, { userId: BigInt(userId) })
  await invalidate(queryKeys.blocks, queryKeys.receivedRequests, queryKeys.sentRequests)
}

export async function unblockUser(userId: string) {
  await api.send(unblockUserEp, { userId: BigInt(userId) })
  await invalidate(queryKeys.blocks)
}
