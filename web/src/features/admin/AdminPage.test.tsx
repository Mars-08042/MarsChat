// @vitest-environment jsdom

import { act } from "react"
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest"
import { MemoryRouter } from "react-router"
import { QueryClientProvider } from "@tanstack/react-query"
import { create } from "@bufbuild/protobuf"
import { api } from "@/api/client"
import { Role, UserSchema, UserStatus, type ListUsersRequest, type User } from "@/gen/account/v1/account_pb"
import { queryClient } from "@/lib/query"
import { useSession } from "@/stores/session"
import { fire, mount, setValue, stubMatchMedia, type Mounted } from "@/test/dom"
import { AdminPage } from "./AdminPage"

vi.mock("@/api/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/client")>()),
  api: { send: vi.fn() },
}))

const send = vi.mocked(api.send) as unknown as ReturnType<typeof vi.fn<(endpoint: unknown, req: ListUsersRequest) => Promise<unknown>>>

const user = (id: bigint, nickname: string, extra: { role?: Role; status?: UserStatus } = {}) =>
  create(UserSchema, { id, nickname, email: `u${id}@example.com`, role: Role.USER, status: UserStatus.NORMAL, ...extra })

const me = user(1n, "阿星", { role: Role.ADMIN })
// 三页数据：游标 0 → 10 → 20，总数 45（每页 20 条，共 3 页）
let pages: Record<string, { users: User[]; nextCursor: bigint }>

let view: Mounted
const text = () => view.container.textContent ?? ""
const button = (label: string) => [...view.container.querySelectorAll("button")].find((b) => b.textContent?.trim() === label)!
const click = async (el: Element) => {
  fire(el, new MouseEvent("click", { bubbles: true }))
  await flush()
}
const flush = () =>
  act(async () => {
    for (let i = 0; i < 5; i++) await new Promise((r) => setTimeout(r, 0))
  })
const lastRequest = () => send.mock.calls.at(-1)![1]

async function render(entry = "/admin") {
  view = mount(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[entry]}>
        <AdminPage />
      </MemoryRouter>
    </QueryClientProvider>,
  )
  await flush()
}

beforeAll(() => stubMatchMedia(false))
beforeEach(() => {
  pages = {
    "0": { users: [me, user(9n, "林晚")], nextCursor: 10n },
    "10": { users: [user(8n, "白露", { status: UserStatus.BANNED })], nextCursor: 20n },
    "20": { users: [user(7n, "宋词")], nextCursor: 0n },
  }
  send.mockReset()
  send.mockImplementation(async (_endpoint, req) => ({ ...pages[req.cursor.toString()], total: 45 }))
  useSession.setState({ userId: "1", user: me })
})
afterEach(() => {
  view.unmount()
  queryClient.clear()
})

describe("AdminPage", () => {
  it("非管理员看到没有权限的说明，不请求列表", async () => {
    useSession.setState({ userId: "9", user: user(9n, "林晚") })
    await render()
    expect(text()).toContain("没有权限访问管理页")
    expect(send).not.toHaveBeenCalled()
  })

  it("显示第一页、总数和页码；自己不能被封禁", async () => {
    await render()
    expect(lastRequest()).toMatchObject({ keyword: "", status: UserStatus.UNSPECIFIED, cursor: 0n, limit: 20 })
    expect(text()).toContain("共 45 人")
    expect(text()).toContain("第 1 / 3 页")
    expect(button("上一页").disabled).toBe(true)
    // 表格和窄屏卡片各渲染一份，第一个「封禁」按钮是自己那一行
    expect(button("封禁").disabled).toBe(true)
  })

  it("按游标前后翻页，最后一页不能再往后", async () => {
    await render()
    await click(button("下一页"))
    expect(lastRequest().cursor).toBe(10n)
    expect(text()).toContain("第 2 / 3 页")
    expect(text()).toContain("白露")
    expect(button("解封")).toBeTruthy()

    await click(button("下一页"))
    expect(lastRequest().cursor).toBe(20n)
    expect(text()).toContain("第 3 / 3 页")
    expect(button("下一页").disabled).toBe(true)

    await click(button("上一页"))
    expect(text()).toContain("第 2 / 3 页")
    expect(text()).toContain("白露")
  })

  it("切换状态筛选后从第一页重新开始，并写进地址", async () => {
    await render()
    await click(button("下一页"))
    await click(button("已封禁"))
    expect(lastRequest()).toMatchObject({ status: UserStatus.BANNED, cursor: 0n })
    expect(text()).toContain("第 1 / 3 页")
  })

  it("地址里的筛选条件作为初始值", async () => {
    await render("/admin?q=lin&status=normal")
    expect(lastRequest()).toMatchObject({ keyword: "lin", status: UserStatus.NORMAL, cursor: 0n })
    expect(view.container.querySelector<HTMLInputElement>("#admin-search")!.value).toBe("lin")
  })

  it("回车立即搜索，关键词去掉首尾空白", async () => {
    await render()
    setValue(view.container.querySelector<HTMLInputElement>("#admin-search")!, "  林 ")
    fire(view.container.querySelector("form[role=search]")!, new Event("submit", { bubbles: true, cancelable: true }))
    await flush()
    expect(lastRequest()).toMatchObject({ keyword: "林", cursor: 0n })
  })

  it("停止输入 300 毫秒后搜索", async () => {
    await render()
    setValue(view.container.querySelector<HTMLInputElement>("#admin-search")!, "bai")
    await flush()
    expect(lastRequest().keyword).toBe("")
    await act(async () => {
      await new Promise((r) => setTimeout(r, 350))
    })
    await flush()
    expect(lastRequest().keyword).toBe("bai")
  })

  it("当前页在操作后变空时退回上一页", async () => {
    await render()
    await click(button("下一页"))
    expect(text()).toContain("第 2 / 3 页")
    pages["10"] = { users: [], nextCursor: 0n }
    await act(async () => {
      await queryClient.invalidateQueries({ queryKey: ["admin", "users"] })
    })
    await flush()
    expect(text()).toContain("第 1 / 3 页")
    expect(text()).toContain("林晚")
  })

  it("筛选后没有结果时提示", async () => {
    send.mockImplementation(async () => ({ users: [], nextCursor: 0n, total: 0 }))
    await render("/admin?q=nobody")
    expect(text()).toContain("没有找到匹配的用户")
  })
})
