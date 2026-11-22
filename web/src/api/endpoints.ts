// 前端用到的 HTTP 接口，路径与 api/*/v1/*.proto 的 google.api.http 注解一致。

import {
  BanUserRequestSchema,
  BanUserResponseSchema,
  GetMeRequestSchema,
  GetMeResponseSchema,
  GetUserRequestSchema,
  GetUserResponseSchema,
  LoginByEmailCodeRequestSchema,
  LoginByEmailCodeResponseSchema,
  LoginByPasswordRequestSchema,
  LoginByPasswordResponseSchema,
  ListSessionsRequestSchema,
  ListSessionsResponseSchema,
  ListUsersRequestSchema,
  ListUsersResponseSchema,
  LogoutRequestSchema,
  LogoutResponseSchema,
  RefreshTokenRequestSchema,
  RefreshTokenResponseSchema,
  RegisterRequestSchema,
  RegisterResponseSchema,
  RevokeSessionRequestSchema,
  RevokeSessionResponseSchema,
  SendEmailCodeRequestSchema,
  SendEmailCodeResponseSchema,
  UnbanUserRequestSchema,
  UnbanUserResponseSchema,
  UpdateMeRequestSchema,
  UpdateMeResponseSchema,
} from "@/gen/account/v1/account_pb"
import {
  GetConversationRequestSchema,
  GetConversationResponseSchema,
  GetDifferenceRequestSchema,
  GetDifferenceResponseSchema,
  ListConversationsRequestSchema,
  ListConversationsResponseSchema,
  ListMessagesRequestSchema,
  ListMessagesResponseSchema,
  MarkReadRequestSchema,
  MarkReadResponseSchema,
} from "@/gen/message/v1/message_pb"
import {
  AcceptFriendRequestRequestSchema,
  AcceptFriendRequestResponseSchema,
  AcceptGroupJoinRequestRequestSchema,
  AcceptGroupJoinRequestResponseSchema,
  AddGroupMembersRequestSchema,
  AddGroupMembersResponseSchema,
  BlockUserRequestSchema,
  BlockUserResponseSchema,
  CreateGroupRequestSchema,
  CreateGroupResponseSchema,
  DeleteFriendRequestSchema,
  DeleteFriendResponseSchema,
  DismissGroupRequestSchema,
  DismissGroupResponseSchema,
  GetGroupRequestSchema,
  GetGroupResponseSchema,
  JoinGroupRequestSchema,
  JoinGroupResponseSchema,
  LeaveGroupRequestSchema,
  LeaveGroupResponseSchema,
  ListBlockedUsersRequestSchema,
  ListBlockedUsersResponseSchema,
  ListFriendsRequestSchema,
  ListFriendsResponseSchema,
  ListGroupJoinRequestsRequestSchema,
  ListGroupJoinRequestsResponseSchema,
  ListGroupMembersRequestSchema,
  ListGroupMembersResponseSchema,
  ListMyGroupsRequestSchema,
  ListMyGroupsResponseSchema,
  ListReceivedFriendRequestsRequestSchema,
  ListReceivedFriendRequestsResponseSchema,
  ListSentFriendRequestsRequestSchema,
  ListSentFriendRequestsResponseSchema,
  MuteGroupMemberRequestSchema,
  MuteGroupMemberResponseSchema,
  RejectFriendRequestRequestSchema,
  RejectFriendRequestResponseSchema,
  RejectGroupJoinRequestRequestSchema,
  RejectGroupJoinRequestResponseSchema,
  RemoveGroupMemberRequestSchema,
  RemoveGroupMemberResponseSchema,
  SendFriendRequestRequestSchema,
  SendFriendRequestResponseSchema,
  SetGroupMemberRoleRequestSchema,
  SetGroupMemberRoleResponseSchema,
  TransferGroupOwnerRequestSchema,
  TransferGroupOwnerResponseSchema,
  UnblockUserRequestSchema,
  UnblockUserResponseSchema,
  UpdateGroupRequestSchema,
  UpdateGroupResponseSchema,
} from "@/gen/social/v1/social_pb"
import { endpoint } from "./http"

export const sendEmailCode = endpoint("POST", "/v1/auth/email-codes", SendEmailCodeRequestSchema, SendEmailCodeResponseSchema, false)
export const register = endpoint("POST", "/v1/auth/register", RegisterRequestSchema, RegisterResponseSchema, false)
export const loginByPassword = endpoint("POST", "/v1/auth/login/password", LoginByPasswordRequestSchema, LoginByPasswordResponseSchema, false)
export const loginByEmailCode = endpoint("POST", "/v1/auth/login/email-code", LoginByEmailCodeRequestSchema, LoginByEmailCodeResponseSchema, false)
export const refreshToken = endpoint("POST", "/v1/auth/refresh", RefreshTokenRequestSchema, RefreshTokenResponseSchema, false)
export const logout = endpoint("POST", "/v1/auth/logout", LogoutRequestSchema, LogoutResponseSchema)
export const getMe = endpoint("GET", "/v1/users/me", GetMeRequestSchema, GetMeResponseSchema)
export const updateMe = endpoint("PATCH", "/v1/users/me", UpdateMeRequestSchema, UpdateMeResponseSchema)
export const getUser = endpoint("GET", "/v1/users/{user_id}", GetUserRequestSchema, GetUserResponseSchema)
export const listSessions = endpoint("GET", "/v1/sessions", ListSessionsRequestSchema, ListSessionsResponseSchema)
export const revokeSession = endpoint("DELETE", "/v1/sessions/{session_id}", RevokeSessionRequestSchema, RevokeSessionResponseSchema)
export const banUser = endpoint("POST", "/v1/admin/users/{user_id}/ban", BanUserRequestSchema, BanUserResponseSchema)
export const unbanUser = endpoint("POST", "/v1/admin/users/{user_id}/unban", UnbanUserRequestSchema, UnbanUserResponseSchema)
export const listUsers = endpoint("GET", "/v1/admin/users", ListUsersRequestSchema, ListUsersResponseSchema)

export const listConversations = endpoint("GET", "/v1/conversations", ListConversationsRequestSchema, ListConversationsResponseSchema)
export const getConversation = endpoint("GET", "/v1/conversations/{conversation_id}", GetConversationRequestSchema, GetConversationResponseSchema)
export const listMessages = endpoint("GET", "/v1/conversations/{conversation_id}/messages", ListMessagesRequestSchema, ListMessagesResponseSchema)
export const getDifference = endpoint("GET", "/v1/conversations/{conversation_id}/difference", GetDifferenceRequestSchema, GetDifferenceResponseSchema)
export const markRead = endpoint("POST", "/v1/conversations/{conversation_id}/read", MarkReadRequestSchema, MarkReadResponseSchema)

export const sendFriendRequest = endpoint("POST", "/v1/friend-requests", SendFriendRequestRequestSchema, SendFriendRequestResponseSchema)
export const listReceivedFriendRequests = endpoint("GET", "/v1/friend-requests/received", ListReceivedFriendRequestsRequestSchema, ListReceivedFriendRequestsResponseSchema)
export const listSentFriendRequests = endpoint("GET", "/v1/friend-requests/sent", ListSentFriendRequestsRequestSchema, ListSentFriendRequestsResponseSchema)
export const acceptFriendRequest = endpoint("POST", "/v1/friend-requests/{request_id}/accept", AcceptFriendRequestRequestSchema, AcceptFriendRequestResponseSchema)
export const rejectFriendRequest = endpoint("POST", "/v1/friend-requests/{request_id}/reject", RejectFriendRequestRequestSchema, RejectFriendRequestResponseSchema)
export const listFriends = endpoint("GET", "/v1/friends", ListFriendsRequestSchema, ListFriendsResponseSchema)
export const deleteFriend = endpoint("DELETE", "/v1/friends/{user_id}", DeleteFriendRequestSchema, DeleteFriendResponseSchema)
export const blockUser = endpoint("POST", "/v1/blocks", BlockUserRequestSchema, BlockUserResponseSchema)
export const unblockUser = endpoint("DELETE", "/v1/blocks/{user_id}", UnblockUserRequestSchema, UnblockUserResponseSchema)
export const listBlockedUsers = endpoint("GET", "/v1/blocks", ListBlockedUsersRequestSchema, ListBlockedUsersResponseSchema)
export const createGroup = endpoint("POST", "/v1/groups", CreateGroupRequestSchema, CreateGroupResponseSchema)
export const getGroup = endpoint("GET", "/v1/groups/{group_id}", GetGroupRequestSchema, GetGroupResponseSchema)
export const updateGroup = endpoint("PATCH", "/v1/groups/{group_id}", UpdateGroupRequestSchema, UpdateGroupResponseSchema)
export const listMyGroups = endpoint("GET", "/v1/groups", ListMyGroupsRequestSchema, ListMyGroupsResponseSchema)
export const joinGroup = endpoint("POST", "/v1/groups/{group_id}/join", JoinGroupRequestSchema, JoinGroupResponseSchema)
export const listGroupJoinRequests = endpoint("GET", "/v1/groups/{group_id}/join-requests", ListGroupJoinRequestsRequestSchema, ListGroupJoinRequestsResponseSchema)
export const acceptGroupJoinRequest = endpoint("POST", "/v1/group-join-requests/{request_id}/accept", AcceptGroupJoinRequestRequestSchema, AcceptGroupJoinRequestResponseSchema)
export const rejectGroupJoinRequest = endpoint("POST", "/v1/group-join-requests/{request_id}/reject", RejectGroupJoinRequestRequestSchema, RejectGroupJoinRequestResponseSchema)
export const addGroupMembers = endpoint("POST", "/v1/groups/{group_id}/members", AddGroupMembersRequestSchema, AddGroupMembersResponseSchema)
export const listGroupMembers = endpoint("GET", "/v1/groups/{group_id}/members", ListGroupMembersRequestSchema, ListGroupMembersResponseSchema)
export const removeGroupMember = endpoint("DELETE", "/v1/groups/{group_id}/members/{user_id}", RemoveGroupMemberRequestSchema, RemoveGroupMemberResponseSchema)
export const setGroupMemberRole = endpoint("PUT", "/v1/groups/{group_id}/members/{user_id}/role", SetGroupMemberRoleRequestSchema, SetGroupMemberRoleResponseSchema)
export const muteGroupMember = endpoint("PUT", "/v1/groups/{group_id}/members/{user_id}/mute", MuteGroupMemberRequestSchema, MuteGroupMemberResponseSchema)
export const transferGroupOwner = endpoint("POST", "/v1/groups/{group_id}/transfer", TransferGroupOwnerRequestSchema, TransferGroupOwnerResponseSchema)
export const leaveGroup = endpoint("POST", "/v1/groups/{group_id}/leave", LeaveGroupRequestSchema, LeaveGroupResponseSchema)
export const dismissGroup = endpoint("POST", "/v1/groups/{group_id}/dismiss", DismissGroupRequestSchema, DismissGroupResponseSchema)
