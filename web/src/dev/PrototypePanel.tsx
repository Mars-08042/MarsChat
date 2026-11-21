import { useState, type ReactNode } from "react"
import { AnimatePresence, motion } from "motion/react"
import { FlaskConical, Play, X } from "lucide-react"
import { router } from "@/app/router"
import type { ConnectionStatus } from "@/components/common/ConnectionPill"
import { useCallStore, type OutgoingOutcome } from "@/features/call/fake-call-store"
import { SUMMIT } from "@/features/chat/fake-images"
import { useChatStore } from "@/features/chat/fake-store"
import { FONT_PAIRS, LOGIN_BACKGROUNDS, useAppearance, type FontPair, type LoginBackground } from "@/lib/appearance"
import { springs } from "@/lib/motion"
import { readThemePreference, saveThemePreference, type ThemePreference } from "@/lib/theme"
import { cn } from "@/lib/utils"

const MB = 1024 * 1024

// 视觉原型阶段的演示面板，只在开发环境挂载：切换外观、手动触发各种状态，方便截图评审。
// 外观偏好（主题、字体、登录页背景）第 6 阶段在设置页提供正式入口
export function PrototypePanel() {
  const [open, setOpen] = useState(false)
  const [theme, setTheme] = useState<ThemePreference>(readThemePreference)
  const { loginBackground, font, setLoginBackground, setFont } = useAppearance()
  const chat = useChatStore()
  const call = useCallStore()

  const connection = (status: ConnectionStatus) => () => chat.setConnection(status)
  const uploadToActive = (file: Parameters<typeof chat.upload>[1]) => {
    if (chat.activeId) chat.upload(chat.activeId, file)
  }

  return (
    // 固定深色，不受页面主题影响；放在右侧边缘中部，尽量不挡住页面元素
    <div className="dark fixed top-1/2 right-0 z-[100] flex -translate-y-1/2 items-center text-foreground">
      <AnimatePresence>
        {open && (
          <motion.div
            initial={{ opacity: 0, x: 16 }}
            animate={{ opacity: 1, x: 0 }}
            exit={{ opacity: 0, x: 16 }}
            transition={springs.smooth}
            className="mr-2 max-h-[90svh] w-80 space-y-4 overflow-y-auto rounded-card border bg-card p-4 text-sm shadow-float [scrollbar-width:thin]"
          >
            <div className="flex items-center justify-between">
              <span className="font-display font-semibold">视觉原型面板</span>
              <button type="button" onClick={() => setOpen(false)} aria-label="收起" className="rounded-md p-1 hover:bg-muted">
                <X className="size-4" />
              </button>
            </div>

            <Section title="页面">
              <Seg options={[["/login", "登录"], ["/register", "注册"], ["/chat/c-linwan", "单聊"], ["/chat/c-frontend", "群聊"]]} onPick={(to) => void router.navigate(to)} />
            </Section>

            <Section title="主题" hint="登录注册页固定深色">
              <Seg<ThemePreference>
                value={theme}
                options={[["light", "亮色"], ["dark", "深色"], ["system", "跟随系统"]]}
                onPick={(t) => {
                  saveThemePreference(t)
                  setTheme(t)
                }}
              />
            </Section>

            <Section title="字体（英文标题 + 等宽）">
              <Seg<FontPair> value={font} options={FONT_PAIRS.map((f) => [f.id, f.label])} onPick={setFont} />
            </Section>

            <Section title="登录页背景">
              <Seg<LoginBackground> value={loginBackground} options={LOGIN_BACKGROUNDS.map((b) => [b.id, b.label])} onPick={setLoginBackground} />
            </Section>

            <Section title="连接状态">
              <div className="flex flex-wrap gap-1.5">
                <Chip onClick={connection({ kind: "connecting" })}>连接中</Chip>
                <Chip onClick={connection({ kind: "syncing", count: 23 })}>同步中</Chip>
                <Chip onClick={connection({ kind: "synced" })}>已同步</Chip>
                <Chip onClick={() => chat.setConnection({ kind: "reconnecting", retryAt: Date.now() + 5000 })}>重连等待</Chip>
                <Chip onClick={connection({ kind: "offline" })}>网络断开</Chip>
                <Chip onClick={connection({ kind: "maintenance" })}>服务器维护</Chip>
                <Chip onClick={chat.playReconnect} accent>
                  <Play className="size-3" />
                  演示断线重连
                </Chip>
              </div>
            </Section>

            <Section title="消息" hint="作用于当前打开的会话">
              <div className="flex flex-wrap gap-1.5">
                <Chip onClick={() => chat.receive("active")}>收到消息</Chip>
                <Chip onClick={() => chat.receive("other")}>其他会话收到消息</Chip>
                <Chip onClick={() => chat.setFailNextSend(!chat.failNextSend)} active={chat.failNextSend}>
                  下一次发送失败
                </Chip>
              </div>
            </Section>

            <Section title="通话" hint="时间已压缩，超时 8 秒">
              <div className="space-y-2">
                <div className="flex flex-wrap gap-1.5">
                  <Chip onClick={() => call.receiveCall(chat.users["1002"], "audio")}>林晚语音来电</Chip>
                  <Chip onClick={() => call.receiveCall(chat.users["1007"], "video")}>Kai 视频来电</Chip>
                </div>
                <p className="text-xs text-muted-foreground">下一次在单聊里呼叫的结果</p>
                <Seg<OutgoingOutcome>
                  value={call.nextOutcome}
                  options={[["accept", "接听"], ["reject", "拒绝"], ["busy", "忙线"], ["offline", "不在线"], ["timeout", "无人接听"]]}
                  onPick={call.setNextOutcome}
                />
                <p className="text-xs text-muted-foreground">通话中对方的动作</p>
                <div className="flex flex-wrap gap-1.5">
                  <Chip onClick={() => call.remoteEnd("hangup")}>对方挂断</Chip>
                  <Chip onClick={() => call.remoteEnd("answered-elsewhere")}>其他设备已接听</Chip>
                  <Chip onClick={() => call.remoteEnd("disconnected")}>连接断开</Chip>
                  <Chip onClick={() => call.remoteEnd("media-failed")}>无法直连</Chip>
                </div>
              </div>
            </Section>

            <Section title="文件" hint="模拟上传速度约 8 MB/s">
              <div className="flex flex-wrap gap-1.5">
                <Chip onClick={() => uploadToActive({ name: "山顶日出.jpg", size: 3.8 * MB, type: "image/jpeg", url: SUMMIT.url, width: SUMMIT.width, height: SUMMIT.height })}>
                  发送图片
                </Chip>
                <Chip onClick={() => uploadToActive({ name: "MarsChat-演示录屏.mp4", size: 64 * MB, type: "video/mp4" })}>发送 64 MB 视频</Chip>
                <Chip onClick={() => uploadToActive({ name: "接口文档.docx", size: 820 * 1024, type: "application/vnd.openxmlformats-officedocument.wordprocessingml.document" })}>
                  发送文档
                </Chip>
              </div>
            </Section>
          </motion.div>
        )}
      </AnimatePresence>
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        className="flex flex-col items-center gap-1.5 rounded-l-xl border border-r-0 bg-card px-1.5 py-3 text-xs text-muted-foreground shadow-float transition-colors hover:text-foreground"
      >
        <FlaskConical className="size-4 text-brand" />
        <span className="[writing-mode:vertical-rl]">原型</span>
      </button>
    </div>
  )
}

function Section({ title, hint, children }: { title: string; hint?: string; children: ReactNode }) {
  return (
    <section className="space-y-2">
      <h3 className="flex items-baseline justify-between text-xs font-medium text-muted-foreground">
        {title}
        {hint && <span className="font-normal">{hint}</span>}
      </h3>
      {children}
    </section>
  )
}

function Seg<T extends string>({ value, options, onPick }: { value?: T; options: Array<[T, string]>; onPick: (value: T) => void }) {
  return (
    <div className="flex flex-wrap gap-1.5">
      {options.map(([v, label]) => (
        <Chip key={v} active={v === value} onClick={() => onPick(v)}>
          {label}
        </Chip>
      ))}
    </div>
  )
}

function Chip({ active, accent, onClick, children }: { active?: boolean; accent?: boolean; onClick: () => void; children: ReactNode }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "inline-flex items-center gap-1 rounded-lg border px-2.5 py-1 text-xs transition-colors",
        active ? "border-brand/50 bg-brand/15 text-brand" : "hover:bg-muted",
        accent && "border-brand/40 text-brand",
      )}
    >
      {children}
    </button>
  )
}
