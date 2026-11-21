// 输入框的发送判断与字数统计（设计 3.6.4）。

// 与 TextContent.text 的 protovalidate 规则一致
export const MAX_TEXT_LENGTH = 4000
// 剩余字数少于这个值时才显示计数，平时保持安静
export const LENGTH_HINT_THRESHOLD = 200

export interface EnterKeyLike {
  key: string
  shiftKey: boolean
  isComposing: boolean
  keyCode: number
}

// Enter 发送，Shift+Enter 换行。输入法选词期间按 Enter 是确认候选词，不能发送。
// composing 来自 compositionstart/compositionend 记录的状态。
// Safari 会在 compositionend 之后才派发这次 Enter 的 keydown，此时 isComposing 已经是 false，
// 只能靠 keyCode 229（输入法处理中）识别。
export function shouldSend(e: EnterKeyLike, composing: boolean): boolean {
  if (e.key !== "Enter" || e.shiftKey) return false
  return !composing && !e.isComposing && e.keyCode !== 229
}

// 后端的 max_len 按 Unicode 码点计数；String.length 按 UTF-16 计数，一个 emoji 会算成 2
export function textLength(text: string): number {
  return [...text].length
}

export function canSend(text: string): boolean {
  return text.trim() !== "" && textLength(text) <= MAX_TEXT_LENGTH
}
