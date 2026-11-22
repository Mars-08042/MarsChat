// 前端用到的 HTTP 接口，路径与 api/*/v1/*.proto 的 google.api.http 注解一致。

import {
  GetMeRequestSchema,
  GetMeResponseSchema,
  GetUserRequestSchema,
  GetUserResponseSchema,
  LoginByEmailCodeRequestSchema,
  LoginByEmailCodeResponseSchema,
  LoginByPasswordRequestSchema,
  LoginByPasswordResponseSchema,
  LogoutRequestSchema,
  LogoutResponseSchema,
  RefreshTokenRequestSchema,
  RefreshTokenResponseSchema,
  RegisterRequestSchema,
  RegisterResponseSchema,
  SendEmailCodeRequestSchema,
  SendEmailCodeResponseSchema,
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
  ListFriendsRequestSchema,
  ListFriendsResponseSchema,
  ListGroupMembersRequestSchema,
  ListGroupMembersResponseSchema,
} from "@/gen/social/v1/social_pb"
import { endpoint } from "./http"

export const sendEmailCode = endpoint("POST", "/v1/auth/email-codes", SendEmailCodeRequestSchema, SendEmailCodeResponseSchema, false)
export const register = endpoint("POST", "/v1/auth/register", RegisterRequestSchema, RegisterResponseSchema, false)
export const loginByPassword = endpoint("POST", "/v1/auth/login/password", LoginByPasswordRequestSchema, LoginByPasswordResponseSchema, false)
export const loginByEmailCode = endpoint("POST", "/v1/auth/login/email-code", LoginByEmailCodeRequestSchema, LoginByEmailCodeResponseSchema, false)
export const refreshToken = endpoint("POST", "/v1/auth/refresh", RefreshTokenRequestSchema, RefreshTokenResponseSchema, false)
export const logout = endpoint("POST", "/v1/auth/logout", LogoutRequestSchema, LogoutResponseSchema)
export const getMe = endpoint("GET", "/v1/users/me", GetMeRequestSchema, GetMeResponseSchema)
export const getUser = endpoint("GET", "/v1/users/{user_id}", GetUserRequestSchema, GetUserResponseSchema)

export const listConversations = endpoint("GET", "/v1/conversations", ListConversationsRequestSchema, ListConversationsResponseSchema)
export const getConversation = endpoint("GET", "/v1/conversations/{conversation_id}", GetConversationRequestSchema, GetConversationResponseSchema)
export const listMessages = endpoint("GET", "/v1/conversations/{conversation_id}/messages", ListMessagesRequestSchema, ListMessagesResponseSchema)
export const getDifference = endpoint("GET", "/v1/conversations/{conversation_id}/difference", GetDifferenceRequestSchema, GetDifferenceResponseSchema)
export const markRead = endpoint("POST", "/v1/conversations/{conversation_id}/read", MarkReadRequestSchema, MarkReadResponseSchema)

export const listFriends = endpoint("GET", "/v1/friends", ListFriendsRequestSchema, ListFriendsResponseSchema)
export const listGroupMembers = endpoint("GET", "/v1/groups/{group_id}/members", ListGroupMembersRequestSchema, ListGroupMembersResponseSchema)
