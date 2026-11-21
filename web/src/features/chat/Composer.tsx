import {
  lazy,
  Suspense,
  useLayoutEffect,
  useRef,
  useState,
  type ClipboardEvent,
  type ComponentProps,
  type KeyboardEvent,
  type ReactNode,
  type RefObject,
} from "react"
import { AnimatePresence, motion } from "motion/react"
import { Popover } from "@base-ui/react/popover"
import { Ban, Delete, FaceSlightlySmiling, FolderOpen, Keyboard } from "lucide-react"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { springs } from "@/lib/motion"
import { DESKTOP_QUERY, useMediaQuery } from "@/lib/use-media-query"
import { cn } from "@/lib/utils"
import { deleteBackward, insertAt } from "./emoji"
import { canSend, LENGTH_HINT_THRESHOLD, MAX_TEXT_LENGTH, shouldSend, textLength } from "./input-rules"

// frimousse 和表情数据只在第一次打开面板时加载
const EmojiPanel = lazy(() => import("./EmojiPanel"))

interface ComposerProps {
  onSend: (text: string) => void
  // 选择、粘贴的文件交给上层统一检查和上传，拖放也走同一个入口
  onFiles: (files: File[]) => void
  // 不能发言的原因，有值时输入框替换为说明条（设计 3.6.4）
  blockedReason?: string
}

export function Composer({ onSend, onFiles, blockedReason }: ComposerProps) {
  if (blockedReason) {
    return (
      <div className="px-4 pt-2 pb-4 md:px-6">
        <div className="flex items-center justify-center gap-2 rounded-card border border-dashed px-4 py-3.5 text-sm text-muted-foreground">
          <Ban className="size-4 shrink-0" />
          {blockedReason}
        </div>
      </div>
    )
  }
  return <ComposerInput onSend={onSend} onFiles={onFiles} />
}

const FILE_HINT = "文件或图片，单个不超过 100 MB；也可以直接拖入或粘贴"

// 桌面端仿微信：上面是多行输入区，下面是工具栏和发送按钮，表情面板从表情按钮上方弹出。
// 手机端是一行：输入框、表情、文件（有内容时换成发送），表情面板顶替软键盘的位置（设计 3.6.4）
function ComposerInput({ onSend, onFiles }: Omit<ComposerProps, "blockedReason">) {
  const desktop = useMediaQuery(DESKTOP_QUERY)
  const [text, setText] = useState("")
  const [emojiOpen, setEmojiOpen] = useState(false)
  const textareaRef = useRef<HTMLTextAreaElement>(null)
  const fileInputRef = useRef<HTMLInputElement>(null)
  // compositionstart 到 compositionend 之间是输入法选词
  const composingRef = useRef(false)
  // 输入框失焦后 selectionStart 仍然有效，但手机端打开面板时会主动失焦，这里另存一份
  const selectionRef = useRef({ start: 0, end: 0 })
  // 文本更新后要恢复的光标位置，在下面的 layout effect 里应用
  const caretRef = useRef<number | null>(null)

  const remaining = MAX_TEXT_LENGTH - textLength(text)
  const sendable = canSend(text)

  // 随内容自动增高，超过 max-height 后内部滚动
  useLayoutEffect(() => {
    const el = textareaRef.current
    if (!el) return
    el.style.height = "auto"
    el.style.height = `${el.scrollHeight}px`
    const caret = caretRef.current
    if (caret !== null) {
      caretRef.current = null
      el.focus()
      el.setSelectionRange(caret, caret)
    }
  }, [text, desktop])

  const rememberSelection = () => {
    const el = textareaRef.current
    if (el) selectionRef.current = { start: el.selectionStart, end: el.selectionEnd }
  }

  const applyEdit = (next: { text: string; caret: number }, focus: boolean) => {
    setText(next.text)
    selectionRef.current = { start: next.caret, end: next.caret }
    if (focus) caretRef.current = next.caret
  }

  const submit = () => {
    if (!canSend(text)) return
    onSend(text.trim())
    setText("")
    selectionRef.current = { start: 0, end: 0 }
    // 手机端从表情面板点发送时保持面板打开，不弹出软键盘
    if (desktop || !emojiOpen) textareaRef.current?.focus()
  }

  // 桌面端选一个就收起面板、回到输入框；手机端面板保持打开，可以连续选
  const insertEmoji = (emoji: string) => {
    const { start, end } = selectionRef.current
    applyEdit(insertAt(text, start, end, emoji), desktop)
    if (desktop) setEmojiOpen(false)
  }

  const toggleMobilePanel = () => {
    if (emojiOpen) {
      setEmojiOpen(false)
      textareaRef.current?.focus()
      return
    }
    rememberSelection()
    // 收起软键盘，面板占用它的位置
    textareaRef.current?.blur()
    setEmojiOpen(true)
  }

  // 粘贴截图或文件时直接发送文件；纯文字粘贴保持默认行为
  const onPaste = (e: ClipboardEvent<HTMLTextAreaElement>) => {
    const files = Array.from(e.clipboardData.files)
    if (files.length === 0) return
    e.preventDefault()
    onFiles(files)
  }

  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    const key = { key: e.key, shiftKey: e.shiftKey, isComposing: e.nativeEvent.isComposing, keyCode: e.keyCode }
    if (!shouldSend(key, composingRef.current)) return
    e.preventDefault()
    submit()
  }

  const textareaProps: ComponentProps<"textarea"> = {
    value: text,
    onChange: (e) => {
      setText(e.target.value)
      selectionRef.current = { start: e.target.selectionStart, end: e.target.selectionEnd }
    },
    onSelect: rememberSelection,
    onKeyDown,
    onPaste,
    onFocus: () => {
      // 手机端点输入框时软键盘弹出，表情面板让位
      if (!desktop) setEmojiOpen(false)
    },
    onCompositionStart: () => {
      composingRef.current = true
    },
    onCompositionEnd: () => {
      composingRef.current = false
    },
    placeholder: "发消息…",
    "aria-label": "消息内容",
  }

  const lengthHint = remaining < LENGTH_HINT_THRESHOLD && (
    <span aria-live="polite" className={cn("shrink-0 font-mono text-xs", remaining < 0 ? "text-destructive" : "text-muted-foreground")}>
      {remaining < 0 ? `超出 ${-remaining} 字` : `还可输入 ${remaining} 字`}
    </span>
  )

  const fileInput = (
    <input
      ref={fileInputRef}
      type="file"
      multiple
      hidden
      onChange={(e) => {
        const files = Array.from(e.target.files ?? [])
        // 清空后同一个文件可以再次选择
        e.target.value = ""
        if (files.length > 0) onFiles(files)
      }}
    />
  )
  const openFilePicker = () => fileInputRef.current?.click()

  if (desktop) {
    return (
      <div className="px-4 pt-2 pb-4 md:px-6">
        <div className="rounded-card border bg-card shadow-card transition-[border-color,box-shadow] focus-within:border-ring/50 focus-within:ring-4 focus-within:ring-ring/10">
          <textarea
            ref={textareaRef}
            rows={2}
            {...textareaProps}
            className="block max-h-[calc(8*1.5rem+1rem)] min-h-[calc(2*1.5rem+1rem)] w-full resize-none bg-transparent px-4 pt-3 pb-1 text-[15px] leading-6 outline-none placeholder:text-muted-foreground [scrollbar-width:thin]"
          />
          <div className="flex items-center gap-0.5 px-2 pb-2">
            <EmojiPopover open={emojiOpen} onOpenChange={setEmojiOpen} onPick={insertEmoji} returnFocusTo={textareaRef} onOpen={rememberSelection} />
            <Tooltip>
              <TooltipTrigger render={<ToolButton onClick={openFilePicker} aria-label="发送文件或图片" />}>
                <FolderOpen className="size-5" />
              </TooltipTrigger>
              <TooltipContent>{FILE_HINT}</TooltipContent>
            </Tooltip>
            {fileInput}
            <div className="ml-auto flex items-center gap-3">
              {lengthHint || <span className="text-[11px] text-muted-foreground">Enter 发送 · Shift + Enter 换行</span>}
              <SendButton enabled={sendable} onClick={submit} className="h-8 rounded-lg px-4" />
            </div>
          </div>
        </div>
      </div>
    )
  }

  const hasText = text.trim() !== ""
  return (
    <div className="pb-[env(safe-area-inset-bottom)]">
      <div className="px-3 pt-2 pb-3">
        {lengthHint && <div className="mb-1 flex justify-end px-2">{lengthHint}</div>}
        <div className="flex items-end gap-1">
          <div className="flex min-w-0 flex-1 items-end rounded-[1.375rem] border bg-card px-3.5 transition-[border-color,box-shadow] focus-within:border-ring/50 focus-within:ring-4 focus-within:ring-ring/10">
            {/* 手机端字号不小于 16px，否则 iOS Safari 聚焦时会自动放大页面 */}
            <textarea
              ref={textareaRef}
              rows={1}
              {...textareaProps}
              className="max-h-[calc(5*1.5rem+1rem)] min-h-10 w-full resize-none bg-transparent py-2 text-base leading-6 outline-none placeholder:text-muted-foreground"
            />
          </div>
          <MobileIconButton onClick={toggleMobilePanel} label={emojiOpen ? "切换到键盘" : "表情"} active={emojiOpen}>
            {emojiOpen ? <Keyboard className="size-6" /> : <FaceSlightlySmiling className="size-6" />}
          </MobileIconButton>
          {/* 有内容时文件按钮换成发送按钮（仿微信） */}
          <AnimatePresence mode="popLayout" initial={false}>
            {hasText ? (
              <motion.div key="send" initial={{ scale: 0.6, opacity: 0 }} animate={{ scale: 1, opacity: 1 }} exit={{ scale: 0.6, opacity: 0 }} transition={springs.snappy}>
                <SendButton enabled={sendable} onClick={submit} className="mb-0.5 h-9 rounded-full px-4" />
              </motion.div>
            ) : (
              <motion.div key="file" initial={{ scale: 0.6, opacity: 0 }} animate={{ scale: 1, opacity: 1 }} exit={{ scale: 0.6, opacity: 0 }} transition={springs.snappy}>
                <MobileIconButton onClick={openFilePicker} label="发送文件或图片">
                  <FolderOpen className="size-6" />
                </MobileIconButton>
              </motion.div>
            )}
          </AnimatePresence>
          {fileInput}
        </div>
      </div>

      <AnimatePresence initial={false}>
        {emojiOpen && (
          <motion.div
            initial={{ height: 0 }}
            animate={{ height: 320 }}
            exit={{ height: 0 }}
            transition={springs.smooth}
            className="overflow-hidden border-t bg-popover"
          >
            <div className="h-[320px]">
              <Suspense fallback={<PanelFallback />}>
                <EmojiPanel
                  columns={7}
                  onPick={insertEmoji}
                  actions={
                    <>
                      <button
                        type="button"
                        onPointerDown={(e) => e.preventDefault()}
                        onClick={() => {
                          const { start, end } = selectionRef.current
                          applyEdit(deleteBackward(text, start, end), false)
                        }}
                        disabled={text === ""}
                        aria-label="删除"
                        className="flex h-8 w-11 items-center justify-center rounded-lg bg-muted text-foreground transition-opacity disabled:opacity-40"
                      >
                        <Delete className="size-5" />
                      </button>
                      <SendButton enabled={sendable} onClick={submit} className="h-8 rounded-lg px-3.5" />
                    </>
                  }
                />
              </Suspense>
            </div>
          </motion.div>
        )}
      </AnimatePresence>
    </div>
  )
}

function EmojiPopover({
  open,
  onOpenChange,
  onPick,
  onOpen,
  returnFocusTo,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  onPick: (emoji: string) => void
  // 打开前记下输入框的选区，插入时用
  onOpen: () => void
  returnFocusTo: RefObject<HTMLTextAreaElement | null>
}) {
  return (
    <Popover.Root
      open={open}
      onOpenChange={(next) => {
        if (next) onOpen()
        onOpenChange(next)
      }}
    >
      <Tooltip>
        <TooltipTrigger render={<Popover.Trigger render={<ToolButton aria-label="表情" />} />}>
          <FaceSlightlySmiling className="size-5" />
        </TooltipTrigger>
        <TooltipContent>表情</TooltipContent>
      </Tooltip>
      {/* 与大图查看器相同：AnimatePresence 控制挂载，Portal 加 keepMounted，退出动画播完才卸载 */}
      <AnimatePresence>
        {open && (
          <Popover.Portal keepMounted>
            <Popover.Positioner side="top" align="start" sideOffset={10} collisionPadding={12} className="z-50">
              <Popover.Popup
                finalFocus={returnFocusTo}
                aria-label="表情"
                render={
                  <motion.div
                    initial={{ opacity: 0, y: 8, scale: 0.96 }}
                    animate={{ opacity: 1, y: 0, scale: 1 }}
                    exit={{ opacity: 0, y: 8, scale: 0.96 }}
                    transition={springs.snappy}
                    style={{ transformOrigin: "var(--transform-origin)" }}
                    className="h-[420px] w-[404px] overflow-hidden rounded-card border bg-popover text-popover-foreground shadow-float outline-none"
                  />
                }
              >
                <Suspense fallback={<PanelFallback />}>
                  <EmojiPanel columns={8} onPick={onPick} />
                </Suspense>
              </Popover.Popup>
            </Popover.Positioner>
          </Popover.Portal>
        )}
      </AnimatePresence>
    </Popover.Root>
  )
}

function PanelFallback() {
  return <div className="flex h-full items-center justify-center text-sm text-muted-foreground">正在加载表情…</div>
}

function ToolButton({ className, ...props }: ComponentProps<"button">) {
  return (
    <button
      type="button"
      {...props}
      className={cn(
        "flex size-9 items-center justify-center rounded-lg text-muted-foreground transition-colors outline-none hover:bg-muted hover:text-foreground focus-visible:ring-3 focus-visible:ring-ring/50 data-popup-open:bg-muted data-popup-open:text-foreground",
        className,
      )}
    />
  )
}

function MobileIconButton({ label, active, onClick, children }: { label: string; active?: boolean; onClick: () => void; children: ReactNode }) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-label={label}
      aria-pressed={active}
      className="mb-0.5 flex size-9 shrink-0 items-center justify-center rounded-full text-foreground/80 transition-colors outline-none active:bg-muted focus-visible:ring-3 focus-visible:ring-ring/50"
    >
      {children}
    </button>
  )
}

function SendButton({ enabled, onClick, className }: { enabled: boolean; onClick: () => void; className?: string }) {
  return (
    <motion.button
      type="button"
      onClick={onClick}
      onPointerDown={(e) => e.preventDefault()}
      disabled={!enabled}
      whileTap={enabled ? { scale: 0.92 } : undefined}
      transition={springs.snappy}
      className={cn(
        "shrink-0 text-sm font-medium transition-colors outline-none focus-visible:ring-3 focus-visible:ring-ring/50",
        enabled ? "bg-primary text-primary-foreground hover:bg-[color-mix(in_oklch,var(--primary),black_14%)]" : "bg-muted text-muted-foreground",
        className,
      )}
    >
      发送
    </motion.button>
  )
}
