// 设备信息（设计 3.8）：同一浏览器的所有标签页共用一个设备 ID，因此共用同一个登录会话。

import type { MessageInitShape } from "@bufbuild/protobuf"
import { Platform, type DeviceSchema } from "@/gen/account/v1/account_pb"
import type { KeyValueStorage } from "./tokens"

export const DEVICE_KEY = "marschat.device"

// 与 Device.device_id 的校验规则一致
const DEVICE_ID_PATTERN = /^[A-Za-z0-9_-]{8,64}$/
const DEVICE_NAME_MAX = 64

// 读取设备 ID，没有或格式不对时生成一个：UUID 去掉连字符后是 32 位十六进制字符
export function loadDeviceId(storage: KeyValueStorage, randomUUID: () => string = () => crypto.randomUUID()): string {
  try {
    const saved: unknown = JSON.parse(storage.getItem(DEVICE_KEY) ?? "null")
    const id = (saved as { id?: unknown } | null)?.id
    if (typeof id === "string" && DEVICE_ID_PATTERN.test(id)) return id
  } catch {
    // 内容损坏时重新生成
  }
  const id = randomUUID().replaceAll("-", "")
  storage.setItem(DEVICE_KEY, JSON.stringify({ id }))
  return id
}

function browserOf(ua: string): string {
  if (/Edg\//.test(ua)) return "Edge"
  if (/OPR\//.test(ua)) return "Opera"
  if (/Firefox\//.test(ua)) return "Firefox"
  if (/Chrome\//.test(ua)) return "Chrome"
  if (/Safari\//.test(ua) && /Version\//.test(ua)) return "Safari"
  return "浏览器"
}

function systemOf(ua: string): string {
  if (/iPhone|iPad|iPod/.test(ua)) return "iOS"
  if (/Android/.test(ua)) return "Android"
  if (/Windows/.test(ua)) return "Windows"
  if (/CrOS/.test(ua)) return "ChromeOS"
  if (/Mac OS X|Macintosh/.test(ua)) return "macOS"
  if (/Linux/.test(ua)) return "Linux"
  return "未知系统"
}

// 设备列表里显示的名称，例如「Chrome · Windows」
export function deviceName(userAgent: string): string {
  return `${browserOf(userAgent)} · ${systemOf(userAgent)}`.slice(0, DEVICE_NAME_MAX)
}

export function deviceInfo(storage: KeyValueStorage, userAgent: string): MessageInitShape<typeof DeviceSchema> {
  return { deviceId: loadDeviceId(storage), platform: Platform.WEB, deviceName: deviceName(userAgent) }
}
