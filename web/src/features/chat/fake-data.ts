// 视觉原型阶段的假数据（计划第 2 阶段）。第 4 阶段接入同步引擎后删除。

import { MARS_DUNES, SCREENSHOT, SUMMIT } from "./fake-images"
import type { ChatMessage, ChatUser, Conversation, MessageContent } from "./types"

export const ME: ChatUser = { id: "1001", nickname: "阿星" }

export const USERS: Record<string, ChatUser> = Object.fromEntries(
  [
    ME,
    { id: "1002", nickname: "林晚" },
    { id: "1003", nickname: "陈屿" },
    { id: "1004", nickname: "周以安" },
    { id: "1005", nickname: "苏念" },
    { id: "1006", nickname: "许知行" },
    { id: "1007", nickname: "Kai" },
    { id: "1008", nickname: "唐果" },
  ].map((u) => [u.id, u]),
)

const MIN = 60_000
const NOW = Date.now()
const minutesAgo = (m: number) => NOW - m * MIN
const daysAgoAt = (days: number, h: number, m: number) => {
  const d = new Date(NOW)
  d.setDate(d.getDate() - days)
  d.setHours(h, m, 0, 0)
  return d.getTime()
}

type Draft = [senderId: string, sentAt: number, content: string | MessageContent, extra?: Partial<ChatMessage>]

let fakeFileId = 7_300_000_000_000_000
const fakeKey = () => `ak_${(fakeFileId++).toString(36)}`

// 假图片消息：localUrl 直接给出图片地址，相当于已经换到了下载地址
function image(name: string, picture: { width: number; height: number; url: string }, size: number): [MessageContent, Partial<ChatMessage>] {
  const meta = { fileId: String(fakeFileId++), accessKey: fakeKey(), name, size, mimeType: "image/jpeg", width: picture.width, height: picture.height }
  return [{ kind: "image", image: meta }, { localUrl: picture.url }]
}

function file(name: string, size: number, mimeType = "application/octet-stream"): MessageContent {
  return { kind: "file", file: { fileId: String(fakeFileId++), accessKey: fakeKey(), name, size, mimeType } }
}

// 按顺序分配 seq；发送中和发送失败的消息没有 seq
function thread(conversationId: string, drafts: Draft[]): ChatMessage[] {
  let seq = 0
  return drafts.map(([senderId, sentAt, content, extra], i) => {
    const status = extra?.status ?? "sent"
    return {
      id: `${conversationId}-m${i}`,
      conversationId,
      senderId,
      sentAt,
      content: typeof content === "string" ? { kind: "text", text: content } : content,
      status,
      seq: status === "sent" ? ++seq : undefined,
      ...extra,
    }
  })
}

const MB = 1024 * 1024
const dunes = image("IMG_20261120_1842.jpg", MARS_DUNES, 3.2 * MB)
const screenshot = image("原型截图.png", SCREENSHOT, 1.4 * MB)
const summit = image("山顶合影.jpg", SUMMIT, 4.6 * MB)

const linWan = thread("c-linwan", [
  ["1002", daysAgoAt(1, 21, 40), "今天的发布顺利吗？"],
  ["1001", daysAgoAt(1, 21, 42), "顺利！灰度到 100% 了，监控一切正常"],
  ["1001", daysAgoAt(1, 21, 42), "就是 access 滚动重启那会儿，所有连接都断了一遍"],
  ["1002", daysAgoAt(1, 21, 44), "那消息没丢吧 😮"],
  ["1001", daysAgoAt(1, 21, 45), "没丢。客户端重连后用 GetDifference 补拉，seq 一条不差，也没有重复"],
  ["1002", daysAgoAt(1, 21, 47), dunes[0], dunes[1]],
  ["1002", daysAgoAt(1, 21, 47), "昨晚拍的晚霞，像不像火星 🌅"],
  ["1002", minutesAgo(48), "早～晚上一起吃火锅吗"],
  ["1002", minutesAgo(47), "新开的那家，听说毛肚很绝"],
  ["1001", minutesAgo(40), "可以！七点？"],
  ["1002", minutesAgo(39), "七点见 🔥"],
  ["1001", minutesAgo(12), "我先过去排号"],
  ["1001", minutesAgo(11), "顺便帮你点个鸳鸯锅"],
  ["1001", minutesAgo(10), file("海底捞菜单（秋季）.pdf", 2.6 * MB, "application/pdf")],
  [
    "1001",
    minutesAgo(3),
    "路上有点堵，可能晚到十分钟",
    { status: "failed", failReason: "网络异常，自动重试已用完" },
  ],
])

const frontend = thread("c-frontend", [
  ["1003", minutesAgo(70), "登录页的背景选好了吗？"],
  ["1004", minutesAgo(69), "我投 Grainient 一票，颗粒感很高级"],
  ["1004", minutesAgo(68), "Silk 有点像屏保 😂"],
  ["1001", minutesAgo(66), "三个都放进原型了，右侧的「原型」面板可以切换"],
  ["1004", minutesAgo(60), screenshot[0], screenshot[1]],
  ["1004", minutesAgo(60), file("frontend-design-v2.pdf", 2.4 * MB, "application/pdf")],
  ["1007", minutesAgo(50), "Space Grotesk 的数字很好看。ID 和时间戳用 JetBrains Mono，工程感一下就出来了"],
  ["1005", minutesAgo(31), "连接状态胶囊太酷了"],
  ["1005", minutesAgo(31), "断线时显示「正在同步 23 条消息」，一眼就知道后端在补拉"],
  ["1006", minutesAgo(24), "输入法打字按回车不会误发了，好评"],
  ["1006", minutesAgo(20), file("marschat-web-dist.zip", 18.6 * MB, "application/zip")],
  ["1003", minutesAgo(9), "今晚合并到 ui 分支？"],
  ["1007", minutesAgo(6), "LGTM 🚀"],
])

const chenYu = thread("c-chenyu", [
  ["1001", daysAgoAt(2, 18, 30), "周末有空吗"],
  ["1003", minutesAgo(130), "周末去爬山吗？天气预报说是晴天"],
  ["1003", minutesAgo(129), "顺便试试新做的登山群 ⛰️"],
])

const hiking = thread("c-hiking", [
  ["1008", daysAgoAt(3, 20, 5), "这周六早上 7 点地铁站集合"],
  ["1004", daysAgoAt(3, 20, 7), "收到，记得带水"],
  ["1001", daysAgoAt(3, 20, 9), "我带相机 📷"],
  ["1008", daysAgoAt(1, 17, 30), summit[0], summit[1]],
  ["1008", daysAgoAt(1, 17, 31), "登顶！下次一起来 ⛰️"],
])

const zhouYiAn = thread("c-zhouyian", [
  ["1004", daysAgoAt(1, 15, 20), "data-flow.md 我更新了一版，你看下第 3 节"],
  ["1001", daysAgoAt(1, 15, 31), "看完了，补拉那段写得很清楚 👍"],
])

const kai = thread("c-kai", [
  ["1007", daysAgoAt(4, 11, 2), "Deploy looks good. p99 latency 38ms"],
  ["1001", daysAgoAt(4, 11, 5), "Nice! Kafka lag 也正常"],
])

const beta = thread("c-beta", [
  ["1006", daysAgoAt(6, 9, 0), "欢迎大家加入 MarsChat 内测 🎉"],
  ["1006", daysAgoAt(6, 9, 1), "反馈问题请带上设备和时间，方便排查"],
  ["1005", minutesAgo(200), "已开启全员禁言，晚上发布新版本"],
])

const tangGuo = thread("c-tangguo", [["1008", daysAgoAt(9, 22, 10), "晚安 🌙"]])

export const INITIAL_MESSAGES: Record<string, ChatMessage[]> = {
  "c-linwan": linWan,
  "c-frontend": frontend,
  "c-chenyu": chenYu,
  "c-hiking": hiking,
  "c-zhouyian": zhouYiAn,
  "c-kai": kai,
  "c-beta": beta,
  "c-tangguo": tangGuo,
}

const lastSeq = (messages: ChatMessage[]) => messages.reduce((max, m) => Math.max(max, m.seq ?? 0), 0)

export const INITIAL_CONVERSATIONS: Record<string, Conversation> = Object.fromEntries(
  (
    [
      // 对方读到了自己发的「七点见」（seq 11），之后我发的三条只是已发送
      { id: "c-linwan", kind: "single", title: "林晚", peerId: "1002", readSeq: lastSeq(linWan), peerReadSeq: 11, unread: 0 },
      {
        id: "c-frontend",
        kind: "group",
        title: "火星基地 · 前端组",
        memberIds: ["1001", "1003", "1004", "1005", "1006", "1007"],
        memberCount: 6,
        readSeq: 7,
        peerReadSeq: 0,
        unread: 6,
      },
      { id: "c-chenyu", kind: "single", title: "陈屿", peerId: "1003", readSeq: 1, peerReadSeq: 1, unread: 2 },
      {
        id: "c-hiking",
        kind: "group",
        title: "周末登山小队",
        memberIds: ["1001", "1004", "1008"],
        memberCount: 9,
        readSeq: lastSeq(hiking),
        peerReadSeq: 0,
        unread: 0,
      },
      { id: "c-zhouyian", kind: "single", title: "周以安", peerId: "1004", readSeq: 2, peerReadSeq: 2, unread: 0 },
      { id: "c-kai", kind: "single", title: "Kai", peerId: "1007", readSeq: 2, peerReadSeq: 2, unread: 0 },
      {
        id: "c-beta",
        kind: "group",
        title: "MarsChat 内测群",
        memberIds: ["1005", "1006"],
        memberCount: 128,
        readSeq: 0,
        peerReadSeq: 0,
        unread: 128,
        blockedReason: "全员禁言中，只有群主和管理员可以发言",
      },
      { id: "c-tangguo", kind: "single", title: "唐果", peerId: "1008", readSeq: 1, peerReadSeq: 0, unread: 0 },
    ] satisfies Conversation[]
  ).map((c) => [c.id, c]),
)

// 模拟收到的消息内容
export const INCOMING_TEXTS = [
  "收到 👌",
  "刚看到，我晚点回你",
  "这个动效好丝滑",
  "断网再连上，消息果然一条不少",
  "今晚 8 点开会，别忘了",
  "已读回执是怎么做的？好神奇",
  "哈哈哈哈哈",
  "Ship it! 🚀",
  "我这边刷新了一下，多端同步也正常",
]

// 发送失败时按会话类型给出原因，演示错误文案（设计 3.6.6）
export const FAIL_REASONS = {
  single: "你们还不是好友，消息未送达",
  group: "你已被禁言，暂时不能发言",
} as const
