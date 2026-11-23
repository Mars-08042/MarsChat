import { describe, expect, it } from "vitest"
import { deviceErrorText, endReasonText, INSECURE_CONTEXT_ERROR, inviteErrorText, type CallEndReason, type EndContext } from "./call-text"

const outgoing: EndContext = { direction: "outgoing", connected: false }
const incoming: EndContext = { direction: "incoming", connected: false }
const talking: EndContext = { direction: "outgoing", connected: true }

describe("endReasonText", () => {
  it("振铃中挂断：被叫看到的是主叫取消", () => {
    expect(endReasonText("hangup", incoming)).toBe("对方已取消")
    expect(endReasonText("hangup", talking)).toBe("对方已挂断")
  })

  it("拒绝与超时按方向区分", () => {
    expect(endReasonText("rejected", outgoing)).toBe("对方拒绝了通话")
    expect(endReasonText("rejected", incoming)).toBe("已拒绝")
    expect(endReasonText("timeout", outgoing)).toBe("对方无人接听")
    expect(endReasonText("timeout", incoming)).toBe("未接来电")
  })

  it("本端结束：接通前是取消，接通后是通话结束", () => {
    expect(endReasonText("local-hangup", outgoing)).toBe("已取消")
    expect(endReasonText("local-hangup", talking)).toBe("通话结束")
  })

  it("每个原因都有文案", () => {
    const reasons: CallEndReason[] = [
      "hangup",
      "rejected",
      "timeout",
      "answered-elsewhere",
      "disconnected",
      "peer-offline",
      "media-failed",
      "local-hangup",
    ]
    for (const r of reasons) expect(endReasonText(r, outgoing)).not.toBe("")
  })
})

describe("inviteErrorText", () => {
  it("已知原因给出具体提示，未知原因给出通用提示", () => {
    expect(inviteErrorText("CALL_BUSY")).toContain("正在通话")
    expect(inviteErrorText("NOT_FRIENDS")).toContain("好友")
    expect(inviteErrorText("SOMETHING_NEW")).toBe("呼叫失败，请稍后重试")
  })
})

describe("deviceErrorText", () => {
  it("按异常名称和通话类型给出提示", () => {
    expect(deviceErrorText("NotAllowedError", "audio")).toContain("麦克风权限")
    expect(deviceErrorText("NotFoundError", "video")).toContain("摄像头或麦克风")
    expect(deviceErrorText("AbortError", "audio")).toBe("无法使用麦克风")
  })

  it("非安全上下文提示改用 HTTPS", () => {
    expect(deviceErrorText(INSECURE_CONTEXT_ERROR, "video")).toBe("当前页面不是 HTTPS 连接，浏览器不允许使用摄像头或麦克风，请改用 HTTPS 地址访问")
  })
})
