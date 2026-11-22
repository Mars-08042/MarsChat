import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { create } from "@bufbuild/protobuf"
import { timestampFromMs } from "@bufbuild/protobuf/wkt"
import { SendResultSchema } from "@/gen/message/v1/message_pb"
import type { ChatMessage } from "@/features/chat/types"
import { createChatStore, draftKey, initialChatState } from "@/stores/chat"
import { fakeServer, TOO_LONG } from "./fake-server"
import { createSyncEngine, DIFFERENCE_LIMIT, extendContiguous, mergeMessages, PAGE_SIZE, READ_DEBOUNCE_MS } from "./sync"

const ME = 1n
const LIN = { id: 2n, nickname: "林晚" }
const CHEN = { id: 3n, nickname: "陈屿" }
const DM = 100n
const GROUP = 200n

function setup() {
  const { server, api } = fakeServer(ME)
  const store = createChatStore()
  store.setState(initialChatState(ME.toString()))
  const progress: number[] = []
  const log = { warn: vi.fn() }
  const engine = createSyncEngine({ store, api, onProgress: (n) => progress.push(n), log })
  const s = () => store.getState()
  const conv = (id: bigint) => s().conversations[id.toString()]
  const timeline = (id: bigint) => s().timelines[id.toString()]
  const seqs = (id: bigint) => timeline(id)?.messages.map((m) => m.seq)
  // 让 open、推送触发的异步请求跑完
  const settle = () => vi.advanceTimersByTimeAsync(0)
  return { server, api, store, engine, progress, log, s, conv, timeline, seqs, settle }
}

const msg = (seq: number, senderId = "2"): ChatMessage => ({
  id: `${senderId}:${seq}`,
  conversationId: "1",
  senderId,
  seq,
  content: { kind: "text", text: String(seq) },
  sentAt: seq,
  status: "sent",
})

beforeEach(() => {
  vi.useFakeTimers()
})
afterEach(() => {
  vi.useRealTimers()
})

describe("时间线合并", () => {
  it("按 seq 去重排序，保留已有的对象", () => {
    const a = msg(2)
    const merged = mergeMessages([msg(1), a], [{ ...msg(2), content: { kind: "text", text: "dup" } }, msg(3)])
    expect(merged.map((m) => m.seq)).toEqual([1, 2, 3])
    expect(merged[1]).toBe(a)
  })

  it("连续 seq 只沿着连续的部分往后走", () => {
    expect(extendContiguous(0, [msg(1), msg(2), msg(4)])).toBe(2)
    expect(extendContiguous(3, [msg(1), msg(4), msg(5)])).toBe(5)
    expect(extendContiguous(0, [msg(3)])).toBe(0)
    expect(extendContiguous(5, [])).toBe(5)
  })
})

describe("会话列表", () => {
  it("分页拉取全部会话，得到标题、未读数和对方资料", async () => {
    const t = setup()
    t.server.addSingle(DM, LIN)
    t.server.postMany(DM, LIN.id, 3)
    t.server.addGroup(GROUP, "火星基地", [{ id: ME, nickname: "阿星" }, LIN, CHEN])
    for (let i = 0; i < 120; i++) t.server.addSingle(1000n + BigInt(i), { id: 5000n + BigInt(i), nickname: `u${i}` })

    await t.engine.syncAll()
    expect(t.server.calls.filter((c) => c === "listConversations")).toHaveLength(2)
    expect(Object.keys(t.s().conversations)).toHaveLength(122)
    expect(t.s().listLoaded).toBe(true)
    expect(t.conv(DM)).toMatchObject({ kind: "single", title: "林晚", peerId: "2", maxSeq: 3, readSeq: 0, unread: 3 })
    expect(t.conv(DM).last?.content).toEqual({ kind: "text", text: "m2" })
    expect(t.conv(GROUP)).toMatchObject({ kind: "group", title: "火星基地", memberCount: 3, unread: 0 })
    expect(t.s().users["2"]).toEqual({ id: "2", nickname: "林晚" })
  })

  it("已解散的群：输入框换成说明条", async () => {
    const t = setup()
    t.server.addGroup(GROUP, "旧群", [], true)
    await t.engine.syncAll()
    expect(t.conv(GROUP).blockedReason).toBe("群已解散，不能发送消息")
  })

  it("不在列表里的会话（退群后）连同时间线一起删除", async () => {
    const t = setup()
    t.server.addGroup(GROUP, "火星基地", [])
    t.server.post(GROUP, LIN.id, "hi")
    await t.engine.syncAll()
    t.engine.open(GROUP.toString())
    await t.settle()
    expect(t.timeline(GROUP)).toBeDefined()

    t.server.remove(GROUP)
    await t.engine.syncAll()
    expect(t.conv(GROUP)).toBeUndefined()
    expect(t.timeline(GROUP)).toBeUndefined()
  })

  it("收到退群通知时直接删除会话、时间线和分隔线记录，再次进群后重新加载成员", async () => {
    const t = setup()
    t.server.addGroup(GROUP, "火星基地", [{ id: ME, nickname: "阿星" }, LIN])
    t.server.post(GROUP, LIN.id, "hi")
    await t.engine.syncAll()
    t.engine.open(GROUP.toString())
    await t.settle()

    t.engine.removeConversation(GROUP.toString())
    expect(t.conv(GROUP)).toBeUndefined()
    expect(t.timeline(GROUP)).toBeUndefined()
    expect(GROUP.toString() in t.s().unreadMarker).toBe(false)

    const before = t.server.calls.filter((c) => c === `listGroupMembers:${GROUP}`).length
    t.engine.open(undefined)
    t.engine.open(GROUP.toString())
    await t.settle()
    expect(t.server.calls.filter((c) => c === `listGroupMembers:${GROUP}`).length).toBe(before + 1)
  })

  it("说明条可以设置和清除，刷新会话列表时保留", async () => {
    const t = setup()
    t.server.addSingle(DM, LIN)
    await t.engine.syncAll()
    t.engine.setBlocked(DM.toString(), "你们还不是好友，不能发送消息")
    expect(t.conv(DM).blockedReason).toBe("你们还不是好友，不能发送消息")
    await t.engine.syncAll()
    expect(t.conv(DM).blockedReason).toBe("你们还不是好友，不能发送消息")
    t.engine.setBlocked(DM.toString(), undefined)
    expect(t.conv(DM).blockedReason).toBeUndefined()
  })
})

describe("打开会话与历史", () => {
  it("加载最新一页，标出第一条未读，群聊加载成员资料", async () => {
    const t = setup()
    t.server.addGroup(GROUP, "火星基地", [{ id: ME, nickname: "阿星" }, LIN, CHEN])
    t.server.postMany(GROUP, LIN.id, 40)
    t.server.postMany(GROUP, CHEN.id, 5)
    await t.engine.syncAll()

    t.engine.open(GROUP.toString())
    await t.settle()
    expect(t.seqs(GROUP)).toEqual(Array.from({ length: PAGE_SIZE }, (_, i) => 16 + i))
    expect(t.timeline(GROUP)).toMatchObject({ contiguousMaxSeq: 45, hasOlder: true })
    // 一条都没读过，第一条未读是已加载的第一条
    expect(t.s().unreadMarker[GROUP.toString()]).toBe(t.timeline(GROUP).messages[0].id)
    expect(t.s().users["3"]).toEqual({ id: "3", nickname: "陈屿" })
  })

  it("打开前分隔线记录被清掉，算好后写回；没有未读时为空", async () => {
    const t = setup()
    t.server.addSingle(DM, LIN)
    t.server.post(DM, ME, "hello")
    await t.engine.syncAll()
    t.engine.open(DM.toString())
    expect(DM.toString() in t.s().unreadMarker).toBe(false)
    await t.settle()
    expect(DM.toString() in t.s().unreadMarker).toBe(true)
    expect(t.s().unreadMarker[DM.toString()]).toBeUndefined()
  })

  it("向前翻页直到没有更早的消息", async () => {
    const t = setup()
    t.server.addSingle(DM, LIN)
    t.server.postMany(DM, LIN.id, 50)
    await t.engine.syncAll()
    t.engine.open(DM.toString())
    await t.settle()
    await t.engine.loadOlder(DM.toString())
    expect(t.seqs(DM)).toEqual(Array.from({ length: 50 }, (_, i) => i + 1))
    expect(t.timeline(DM)).toMatchObject({ hasOlder: false, loadingOlder: false, contiguousMaxSeq: 50 })
  })

  it("通过地址直接打开不在列表里的会话：先取会话信息", async () => {
    const t = setup()
    await t.engine.syncAll()
    t.server.addSingle(DM, LIN)
    t.server.post(DM, LIN.id, "hi")
    t.engine.open(DM.toString())
    await t.settle()
    expect(t.server.calls).toContain(`getConversation:${DM}`)
    expect(t.seqs(DM)).toEqual([1])
  })
})

describe("推送", () => {
  async function opened(n = 3) {
    const t = setup()
    t.server.addSingle(DM, LIN)
    t.server.postMany(DM, LIN.id, n)
    await t.engine.syncAll()
    t.engine.open(DM.toString())
    await t.settle()
    return t
  }

  it("seq 连续：追加；重复的 seq 丢弃", async () => {
    const t = await opened(3)
    const m4 = t.server.post(DM, LIN.id, "four")
    t.engine.handleMessage(m4)
    t.engine.handleMessage(m4)
    expect(t.seqs(DM)).toEqual([1, 2, 3, 4])
    expect(t.conv(DM)).toMatchObject({ maxSeq: 4, unread: 4 })
    expect(t.server.calls.some((c) => c.startsWith("getDifference"))).toBe(false)
  })

  it("seq 有缺口：补拉中间缺的消息", async () => {
    const t = await opened(3)
    t.server.post(DM, LIN.id, "four")
    t.server.post(DM, LIN.id, "five")
    const m6 = t.server.post(DM, LIN.id, "six")
    t.engine.handleMessage(m6)
    await t.settle()
    expect(t.server.calls).toContain(`getDifference:${DM}:3`)
    expect(t.seqs(DM)).toEqual([1, 2, 3, 4, 5, 6])
    expect(t.timeline(DM).contiguousMaxSeq).toBe(6)
  })

  it("补拉期间又出现缺口：结束后再补一轮", async () => {
    const t = await opened(1)
    t.server.post(DM, LIN.id, "2")
    const m3 = t.server.post(DM, LIN.id, "3")
    t.engine.handleMessage(m3)
    t.server.post(DM, LIN.id, "4")
    const m5 = t.server.post(DM, LIN.id, "5")
    t.engine.handleMessage(m5)
    await t.settle()
    expect(t.seqs(DM)).toEqual([1, 2, 3, 4, 5])
  })

  it("自己在其他标签页发的消息：推进自己的已读位置，不算未读", async () => {
    const t = await opened(2)
    const mine = t.server.post(DM, ME, "from another tab")
    t.engine.handleMessage(mine)
    expect(t.conv(DM)).toMatchObject({ maxSeq: 3, readSeq: 3, unread: 0 })
  })

  it("没打开过的会话：只更新摘要和未读数，不拉消息", async () => {
    const t = setup()
    t.server.addSingle(DM, LIN)
    await t.engine.syncAll()
    t.engine.handleMessage(t.server.post(DM, LIN.id, "hi"))
    await t.settle()
    expect(t.conv(DM)).toMatchObject({ maxSeq: 1, unread: 1 })
    expect(t.conv(DM).last?.content).toEqual({ kind: "text", text: "hi" })
    expect(t.timeline(DM)).toBeUndefined()
  })

  it("不认识的会话：取会话信息后插入列表", async () => {
    const t = setup()
    await t.engine.syncAll()
    t.server.addGroup(GROUP, "新群", [])
    t.engine.handleMessage(t.server.post(GROUP, LIN.id, "大家好"))
    await t.settle()
    expect(t.conv(GROUP)).toMatchObject({ title: "新群", maxSeq: 1, unread: 1 })
  })

  it("已读推送：本人的更新已读位置，对方的更新已读回执，较小的值忽略", async () => {
    const t = await opened(5)
    t.engine.handleRead(t.server.readPosition(DM, ME, 4n))
    expect(t.conv(DM)).toMatchObject({ readSeq: 4, unread: 1 })
    t.engine.handleRead(t.server.readPosition(DM, ME, 2n))
    expect(t.conv(DM).readSeq).toBe(4)
    t.engine.handleRead(t.server.readPosition(DM, LIN.id, 5n))
    t.engine.handleRead(t.server.readPosition(DM, LIN.id, 3n))
    expect(t.conv(DM).peerReadSeq).toBe(5)
  })
})

describe("重连后的同步", () => {
  it("只为打开过的会话补拉，补拉条数计入进度", async () => {
    const t = setup()
    t.server.addSingle(DM, LIN)
    t.server.addGroup(GROUP, "火星基地", [])
    t.server.postMany(DM, LIN.id, 2)
    await t.engine.syncAll()
    t.engine.open(DM.toString())
    await t.settle()

    // 断线期间两个会话都有新消息
    t.server.postMany(DM, LIN.id, 7)
    t.server.postMany(GROUP, CHEN.id, 4)
    await t.engine.syncAll()
    expect(t.seqs(DM)).toEqual([1, 2, 3, 4, 5, 6, 7, 8, 9])
    expect(t.conv(GROUP)).toMatchObject({ maxSeq: 4, unread: 4 })
    expect(t.server.calls.some((c) => c.startsWith(`getDifference:${GROUP}`))).toBe(false)
    expect(t.progress.at(-1)).toBe(7)
  })

  it("SLICE：用下一段的起点继续拉，直到 COMPLETE", async () => {
    const t = setup()
    t.server.addSingle(DM, LIN)
    t.server.post(DM, LIN.id, "first")
    await t.engine.syncAll()
    t.engine.open(DM.toString())
    await t.settle()

    t.server.postMany(DM, LIN.id, 450)
    await t.engine.syncAll()
    expect(t.server.calls.filter((c) => c.startsWith("getDifference"))).toEqual([
      `getDifference:${DM}:1`,
      `getDifference:${DM}:${1 + DIFFERENCE_LIMIT}`,
      `getDifference:${DM}:${1 + 2 * DIFFERENCE_LIMIT}`,
    ])
    expect(t.timeline(DM).messages).toHaveLength(451)
    expect(t.timeline(DM).contiguousMaxSeq).toBe(451)
    expect(t.progress.at(-1)).toBe(450)
  })

  it("TOO_LONG：丢弃旧时间线，重新加载最新一页并标注", async () => {
    const t = setup()
    t.server.addSingle(DM, LIN)
    t.server.post(DM, LIN.id, "first")
    await t.engine.syncAll()
    t.engine.open(DM.toString())
    await t.settle()

    t.server.postMany(DM, LIN.id, TOO_LONG + 5)
    await t.engine.syncAll()
    const total = TOO_LONG + 6
    expect(t.seqs(DM)).toEqual(Array.from({ length: PAGE_SIZE }, (_, i) => total - PAGE_SIZE + 1 + i))
    expect(t.timeline(DM)).toMatchObject({ jumped: true, hasOlder: true, contiguousMaxSeq: total })
  })
})

describe("发送确认与已读上报", () => {
  const sending = (conversationId: string, clientMsgId: string): ChatMessage => ({
    id: `1:${clientMsgId}`,
    conversationId,
    senderId: "1",
    content: { kind: "text", text: "hi" },
    sentAt: 0,
    status: "pending",
  })

  it("确认后从发送中移到时间线，推进自己的已读位置", async () => {
    const t = setup()
    t.server.addSingle(DM, LIN)
    t.server.postMany(DM, LIN.id, 2)
    await t.engine.syncAll()
    t.engine.open(DM.toString())
    await t.settle()

    const local = sending(DM.toString(), "x1")
    t.store.setState({ outbox: { [DM.toString()]: [local] } })
    t.server.post(DM, ME, "hi", "x1")
    t.engine.confirmSent(local, create(SendResultSchema, { conversationId: DM, seq: 3n, createdAt: timestampFromMs(5000) }))
    expect(t.s().outbox).toEqual({})
    expect(t.timeline(DM).messages.at(-1)).toMatchObject({ id: "1:x1", seq: 3, status: "sent", sentAt: 5000 })
    expect(t.conv(DM)).toMatchObject({ maxSeq: 3, readSeq: 3, unread: 0 })
  })

  it("草稿单聊的第一条消息：建立会话、记下会话 ID，其余发送中的消息挂到新会话", async () => {
    const t = setup()
    await t.engine.syncAll()
    t.store.setState((s) => ({ users: { ...s.users, "2": { id: "2", nickname: "林晚" } } }))
    const key = draftKey("2")
    const first = sending(key, "d1")
    const second = sending(key, "d2")
    t.store.setState({ outbox: { [key]: [first, second] } })

    t.server.addSingle(DM, LIN)
    t.server.post(DM, ME, "hi", "d1")
    t.engine.confirmSent(first, create(SendResultSchema, { conversationId: DM, seq: 1n }))
    expect(t.s().draftResolved["2"]).toBe(DM.toString())
    expect(t.conv(DM)).toMatchObject({ kind: "single", title: "林晚", peerId: "2" })
    expect(t.seqs(DM)).toEqual([1])
    expect(t.s().outbox).toEqual({ [DM.toString()]: [{ ...second, conversationId: DM.toString() }] })
    await t.settle()
    expect(t.server.calls).toContain(`getConversation:${DM}`)
  })

  it("已读上报防抖：多次调用只上报一次，取最大的 seq", async () => {
    const t = setup()
    t.server.addSingle(DM, LIN)
    t.server.postMany(DM, LIN.id, 4)
    await t.engine.syncAll()
    t.engine.open(DM.toString())
    await t.settle()

    t.engine.reportRead(DM.toString())
    t.engine.handleMessage(t.server.post(DM, LIN.id, "5"))
    t.engine.reportRead(DM.toString())
    await vi.advanceTimersByTimeAsync(READ_DEBOUNCE_MS - 1)
    expect(t.server.calls.filter((c) => c.startsWith("markRead"))).toEqual([])
    await vi.advanceTimersByTimeAsync(1)
    expect(t.server.calls.filter((c) => c.startsWith("markRead"))).toEqual([`markRead:${DM}:5`])
    expect(t.conv(DM)).toMatchObject({ readSeq: 5, unread: 0 })
    expect(t.server.readOf(DM, ME)).toBe(5n)
  })

  it("已经读到最新时不再上报", async () => {
    const t = setup()
    t.server.addSingle(DM, LIN)
    t.server.post(DM, ME, "mine")
    await t.engine.syncAll()
    t.engine.open(DM.toString())
    await t.settle()
    t.engine.reportRead(DM.toString())
    await vi.advanceTimersByTimeAsync(READ_DEBOUNCE_MS)
    expect(t.server.calls.filter((c) => c.startsWith("markRead"))).toEqual([])
  })
})
