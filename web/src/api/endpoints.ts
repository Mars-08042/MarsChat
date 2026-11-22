// 前端用到的 HTTP 接口，路径与 api/*/v1/*.proto 的 google.api.http 注解一致。

import {
  GetMeRequestSchema,
  GetMeResponseSchema,
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
import { endpoint } from "./http"

export const sendEmailCode = endpoint("POST", "/v1/auth/email-codes", SendEmailCodeRequestSchema, SendEmailCodeResponseSchema, false)
export const register = endpoint("POST", "/v1/auth/register", RegisterRequestSchema, RegisterResponseSchema, false)
export const loginByPassword = endpoint("POST", "/v1/auth/login/password", LoginByPasswordRequestSchema, LoginByPasswordResponseSchema, false)
export const loginByEmailCode = endpoint("POST", "/v1/auth/login/email-code", LoginByEmailCodeRequestSchema, LoginByEmailCodeResponseSchema, false)
export const refreshToken = endpoint("POST", "/v1/auth/refresh", RefreshTokenRequestSchema, RefreshTokenResponseSchema, false)
export const logout = endpoint("POST", "/v1/auth/logout", LogoutRequestSchema, LogoutResponseSchema)
export const getMe = endpoint("GET", "/v1/users/me", GetMeRequestSchema, GetMeResponseSchema)
