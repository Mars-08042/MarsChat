// @vitest-environment jsdom
// queries.ts 引用了 HTTP 客户端，令牌管理在导入时读取 localStorage，所以用 jsdom 环境

import { describe, expect, it } from "vitest"
import { create } from "@bufbuild/protobuf"
import { timestampFromMs } from "@bufbuild/protobuf/wkt"
import { Gender, Platform, SessionSchema } from "@/gen/account/v1/account_pb"
import { genderOf, sortDevices, toDevice } from "./queries"

const session = (id: bigint, init: { platform?: Platform; lastActiveAt?: number; current?: boolean; deviceName?: string } = {}) =>
  create(SessionSchema, {
    id,
    platform: init.platform ?? Platform.WEB,
    deviceName: init.deviceName ?? "Chrome · Windows",
    lastIp: "10.0.0.1",
    createdAt: timestampFromMs(1_000),
    lastActiveAt: init.lastActiveAt === undefined ? undefined : timestampFromMs(init.lastActiveAt),
    current: init.current ?? false,
  })

describe("toDevice", () => {
  it("转换平台、时间与当前设备标记", () => {
    expect(toDevice(session(7n, { platform: Platform.IOS, lastActiveAt: 5_000, current: true }))).toEqual({
      id: "7",
      platform: "ios",
      name: "Chrome · Windows",
      lastIp: "10.0.0.1",
      createdAt: 1_000,
      lastActiveAt: 5_000,
      current: true,
    })
  })

  it("未知平台和缺失的时间保留为空", () => {
    const d = toDevice(session(8n, { platform: Platform.UNSPECIFIED }))
    expect(d.platform).toBe("unknown")
    expect(d.lastActiveAt).toBeUndefined()
  })
})

describe("sortDevices", () => {
  it("当前设备在最前，其余按最近活跃时间倒序，没有时间的排最后", () => {
    const list = [
      toDevice(session(1n, { lastActiveAt: 100 })),
      toDevice(session(2n)),
      toDevice(session(3n, { lastActiveAt: 300 })),
      toDevice(session(4n, { lastActiveAt: 50, current: true })),
    ]
    expect(sortDevices(list).map((d) => d.id)).toEqual(["4", "3", "1", "2"])
  })
})

describe("genderOf", () => {
  it("把接口的性别映射为选项", () => {
    expect(genderOf(Gender.MALE)).toBe("male")
    expect(genderOf(Gender.FEMALE)).toBe("female")
    expect(genderOf(Gender.UNSPECIFIED)).toBe("unspecified")
  })
})
