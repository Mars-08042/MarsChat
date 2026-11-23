// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { create } from "@bufbuild/protobuf"
import { Gender, UserSchema } from "@/gen/account/v1/account_pb"
import { useSession } from "@/stores/session"
import { fire, mount, setValue, type Mounted } from "@/test/dom"
import { ProfileTab } from "./ProfileTab"
import { updateProfile } from "./queries"

vi.mock("./queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./queries")>()),
  updateProfile: vi.fn(async () => undefined),
}))

const user = create(UserSchema, {
  id: 2n,
  email: "lin@example.com",
  nickname: "林晚",
  gender: Gender.FEMALE,
  birthday: "1999-05-20",
  signature: "晚风很温柔",
})

let view: Mounted
const q = <T extends Element>(selector: string) => view.container.querySelector<T>(selector)!
const button = (text: string) => [...view.container.querySelectorAll("button")].find((b) => b.textContent?.trim() === text)!

beforeEach(() => {
  vi.mocked(updateProfile).mockClear()
  useSession.setState({ userId: "2", user })
  view = mount(<ProfileTab />)
})
afterEach(() => view.unmount())

describe("ProfileTab", () => {
  it("没有修改时不能保存", () => {
    expect(button("保存").disabled).toBe(true)
    expect(button("撤销修改").disabled).toBe(true)
  })

  it("只发送修改过的字段，昵称去掉首尾空白", () => {
    setValue(q<HTMLInputElement>("#profile-nickname"), "  晚晚 ")
    fire(button("男"), new MouseEvent("click", { bubbles: true }))
    fire(q("form"), new Event("submit", { bubbles: true, cancelable: true }))
    expect(updateProfile).toHaveBeenCalledWith({ nickname: "晚晚", gender: "male", birthday: undefined, signature: undefined })
  })

  it("清除生日时发送空字符串", () => {
    fire(button("清除"), new MouseEvent("click", { bubbles: true }))
    fire(q("form"), new Event("submit", { bubbles: true, cancelable: true }))
    expect(updateProfile).toHaveBeenCalledWith({ nickname: undefined, gender: undefined, birthday: "", signature: undefined })
  })

  it("昵称为空或超过 16 个字时不能保存", () => {
    setValue(q<HTMLInputElement>("#profile-nickname"), "   ")
    expect(view.container.textContent).toContain("昵称不能为空")
    expect(button("保存").disabled).toBe(true)
    setValue(q<HTMLInputElement>("#profile-nickname"), "火".repeat(17))
    expect(button("保存").disabled).toBe(true)
    fire(q("form"), new Event("submit", { bubbles: true, cancelable: true }))
    expect(updateProfile).not.toHaveBeenCalled()
  })

  it("撤销修改恢复原来的资料", () => {
    setValue(q<HTMLTextAreaElement>("#profile-signature"), "新的签名")
    fire(button("撤销修改"), new MouseEvent("click", { bubbles: true }))
    expect(q<HTMLTextAreaElement>("#profile-signature").value).toBe("晚风很温柔")
    expect(button("保存").disabled).toBe(true)
  })
})
