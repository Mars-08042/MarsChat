// 设置页的数据：自己的资料修改、登录设备列表与踢设备（设计 3.6.8）。

import { useQuery } from "@tanstack/react-query"
import { timestampMs } from "@bufbuild/protobuf/wkt"
import { api } from "@/api/client"
import { listSessions, revokeSession, updateMe } from "@/api/endpoints"
import { Gender, Platform, type Session, type User } from "@/gen/account/v1/account_pb"
import { queryClient, queryKeys } from "@/lib/query"
import { chatStore } from "@/stores/chat"
import { useSession } from "@/stores/session"

export type GenderChoice = "unspecified" | "male" | "female"

export const GENDER_OPTIONS: readonly { id: GenderChoice; label: string; value: Gender }[] = [
  { id: "unspecified", label: "不填", value: Gender.UNSPECIFIED },
  { id: "male", label: "男", value: Gender.MALE },
  { id: "female", label: "女", value: Gender.FEMALE },
]

export const genderOf = (g: Gender): GenderChoice => GENDER_OPTIONS.find((o) => o.value === g)?.id ?? "unspecified"

// 与 UpdateMeRequest 的校验一致：昵称 1～16 个字，签名最多 100 个字（按码点计）
export const NICKNAME_MAX = 16
export const SIGNATURE_MAX = 100
// 生日不早于这一天、不晚于今天（account 按北京时间判断「今天」）
export const BIRTHDAY_MIN = "1900-01-01"

export interface ProfilePatch {
  nickname?: string
  gender?: GenderChoice
  // 空字符串表示清除
  birthday?: string
  signature?: string
}

// 只发送有变化的字段，返回修改后的完整资料；同时更新登录状态和聊天里自己的资料
export async function updateProfile(patch: ProfilePatch): Promise<User | undefined> {
  const gender = patch.gender && GENDER_OPTIONS.find((o) => o.id === patch.gender)?.value
  const { user } = await api.send(updateMe, {
    nickname: patch.nickname,
    gender,
    birthday: patch.birthday,
    signature: patch.signature,
  })
  if (!user) return undefined
  const id = user.id.toString()
  if (useSession.getState().userId === id) useSession.setState({ user })
  // 群成员列表、资料卡里自己的资料来自聊天 store 和资料查询，一并更新
  chatStore.setState((s) =>
    s.users[id]
      ? {
          users: {
            ...s.users,
            [id]: {
              ...s.users[id],
              nickname: user.nickname,
              gender: user.gender === Gender.MALE ? "male" : user.gender === Gender.FEMALE ? "female" : undefined,
              signature: user.signature || undefined,
            },
          },
        }
      : {},
  )
  void queryClient.invalidateQueries({ queryKey: queryKeys.user(id) })
  return user
}

export type DevicePlatform = "web" | "android" | "ios" | "desktop" | "unknown"

export interface DeviceItem {
  id: string
  platform: DevicePlatform
  name: string
  lastIp: string
  createdAt?: number
  lastActiveAt?: number
  current: boolean
}

const PLATFORM: Partial<Record<Platform, DevicePlatform>> = {
  [Platform.WEB]: "web",
  [Platform.ANDROID]: "android",
  [Platform.IOS]: "ios",
  [Platform.DESKTOP]: "desktop",
}

export function toDevice(s: Session): DeviceItem {
  return {
    id: s.id.toString(),
    platform: PLATFORM[s.platform] ?? "unknown",
    name: s.deviceName,
    lastIp: s.lastIp,
    createdAt: s.createdAt ? timestampMs(s.createdAt) : undefined,
    lastActiveAt: s.lastActiveAt ? timestampMs(s.lastActiveAt) : undefined,
    current: s.current,
  }
}

// 当前设备排在最前，其余按最近活跃时间倒序
export function sortDevices(list: DeviceItem[]): DeviceItem[] {
  return [...list].sort((a, b) => Number(b.current) - Number(a.current) || (b.lastActiveAt ?? 0) - (a.lastActiveAt ?? 0))
}

async function fetchDevices(): Promise<DeviceItem[]> {
  const res = await api.send(listSessions, {})
  return sortDevices(res.sessions.map(toDevice))
}

// 设备列表随时可能被别的设备改变（新登录、被踢），打开设备页时总是重新获取
export const useDevices = () => useQuery({ queryKey: queryKeys.sessions, queryFn: fetchDevices, staleTime: 0 })

// 踢掉设备：那台设备的连接立即收到 Kicked 并断开，刷新令牌随即失效
export async function kickDevice(sessionId: string) {
  try {
    await api.send(revokeSession, { sessionId: BigInt(sessionId) })
  } finally {
    // 已经下线（SESSION_NOT_FOUND）时也刷新，列表里那台设备随之消失
    void queryClient.invalidateQueries({ queryKey: queryKeys.sessions })
  }
}
