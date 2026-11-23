// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest"
import { act } from "react"
import { mount, type Mounted } from "@/test/dom"
import { NotificationsTab } from "./NotificationsTab"

// jsdom 没有 Notification；用可控的假实现模拟各种权限
function stubNotification(permission: NotificationPermission, afterRequest: NotificationPermission = permission) {
  const fake = Object.assign(vi.fn(), {
    permission,
    requestPermission: vi.fn(async () => {
      fake.permission = afterRequest
      return afterRequest
    }),
  })
  vi.stubGlobal("Notification", fake)
  return fake
}

function setSecure(secure: boolean) {
  Object.defineProperty(window, "isSecureContext", { value: secure, configurable: true })
}

let view: Mounted | undefined
const text = () => view!.container.textContent ?? ""
const button = (label: string) => [...view!.container.querySelectorAll("button")].find((b) => b.textContent?.trim() === label)

afterEach(() => {
  view?.unmount()
  view = undefined
  vi.unstubAllGlobals()
})

describe("NotificationsTab", () => {
  it("还没有授权时可以申请，授权后显示已开启和测试按钮", async () => {
    setSecure(true)
    const fake = stubNotification("default", "granted")
    view = mount(<NotificationsTab />)
    expect(text()).toContain("还没有开启")

    await act(async () => button("允许通知")!.click())
    expect(fake.requestPermission).toHaveBeenCalledTimes(1)
    expect(text()).toContain("已开启")
    expect(button("允许通知")).toBeUndefined()

    act(() => button("发送测试通知")!.click())
    expect(fake).toHaveBeenCalledWith("MarsChat", expect.objectContaining({ tag: "marschat-test" }))
  })

  it("已被阻止时只给出修改网站设置的说明，不再申请", () => {
    setSecure(true)
    stubNotification("denied")
    view = mount(<NotificationsTab />)
    expect(text()).toContain("已被浏览器阻止")
    expect(button("允许通知")).toBeUndefined()
  })

  it("非 HTTPS 访问时提示改用 HTTPS", () => {
    setSecure(false)
    stubNotification("default")
    view = mount(<NotificationsTab />)
    expect(text()).toContain("需要通过 HTTPS")
    expect(button("允许通知")).toBeUndefined()
  })

  it("浏览器不支持通知", () => {
    setSecure(true)
    vi.stubGlobal("Notification", undefined)
    view = mount(<NotificationsTab />)
    expect(text()).toContain("不支持系统通知")
  })

  it("切回页面时重新读取权限（用户可能在网站设置里改了）", () => {
    setSecure(true)
    const fake = stubNotification("denied")
    view = mount(<NotificationsTab />)
    fake.permission = "granted"
    act(() => {
      window.dispatchEvent(new Event("focus"))
    })
    expect(text()).toContain("已开启")
  })
})
