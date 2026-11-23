import { describe, expect, it } from "vitest"
import { Platform } from "@/gen/account/v1/account_pb"
import { DEVICE_KEY, deviceInfo, deviceName, loadDeviceId } from "./device"
import { memoryStorage } from "./test-utils"

const UUID = "123e4567-e89b-12d3-a456-426614174000"

describe("loadDeviceId", () => {
  it("首次生成：UUID 去掉连字符，保存后重复使用", () => {
    const storage = memoryStorage()
    let calls = 0
    const gen = () => {
      calls++
      return UUID
    }
    const id = loadDeviceId(storage, gen)
    expect(id).toBe("123e4567e89b12d3a456426614174000")
    expect(id).toMatch(/^[A-Za-z0-9_-]{8,64}$/)
    expect(JSON.parse(storage.getItem(DEVICE_KEY)!)).toEqual({ id })
    expect(loadDeviceId(storage, gen)).toBe(id)
    expect(calls).toBe(1)
  })

  it.each([
    ["格式不对", JSON.stringify({ id: "has space" })],
    ["太短", JSON.stringify({ id: "abc" })],
    ["不是对象", JSON.stringify("abc12345")],
    ["JSON 损坏", "{"],
  ])("%s时重新生成", (_, raw) => {
    const storage = memoryStorage({ [DEVICE_KEY]: raw })
    expect(loadDeviceId(storage, () => UUID)).toBe("123e4567e89b12d3a456426614174000")
  })
})

describe("deviceName", () => {
  it.each([
    [
      "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36 Edg/142.0.0.0",
      "Edge · Windows",
    ],
    [
      "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36",
      "Chrome · macOS",
    ],
    [
      "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1",
      "Safari · iOS",
    ],
    ["Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0", "Firefox · Linux"],
    [
      "Mozilla/5.0 (Linux; Android 15; Pixel 9) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Mobile Safari/537.36",
      "Chrome · Android",
    ],
    ["curl/8.0", "浏览器 · 未知系统"],
  ])("%s", (ua, want) => {
    expect(deviceName(ua)).toBe(want)
  })
})

it("deviceInfo 固定为 Web 平台", () => {
  const info = deviceInfo(memoryStorage(), "Mozilla/5.0 (Windows NT 10.0) Chrome/142.0")
  expect(info.platform).toBe(Platform.WEB)
  expect(info.deviceName).toBe("Chrome · Windows")
  expect(info.deviceId).toMatch(/^[0-9a-f]{32}$/)
})
