import { useState, type ReactNode } from "react"
import { errorText } from "@/api/errors"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { AppDialog } from "./AppDialog"

interface ConfirmDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: ReactNode
  description: ReactNode
  confirmLabel: string
  destructive?: boolean
  // 需要输入这段文字才能确认，例如解散群时输入群名（设计 3.6.7）
  confirmText?: string
  // 失败时抛出错误，对话框保持打开并显示原因；errorOverrides 按场景替换错误文案
  onConfirm: () => Promise<void>
  errorOverrides?: Readonly<Record<string, string>>
}

// 删除好友、移除成员、转让、退群、解散等操作的二次确认
export function ConfirmDialog(props: ConfirmDialogProps) {
  return (
    <AppDialog open={props.open} onOpenChange={props.onOpenChange} title={props.title}>
      {/* 每次打开重新挂载，清掉上一次的输入和错误 */}
      {props.open && <ConfirmBody {...props} />}
    </AppDialog>
  )
}

function ConfirmBody({ onOpenChange, description, confirmLabel, destructive, confirmText, onConfirm, errorOverrides }: ConfirmDialogProps) {
  const [typed, setTyped] = useState("")
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string>()
  const matched = confirmText === undefined || typed.trim() === confirmText

  const confirm = async () => {
    setPending(true)
    setError(undefined)
    try {
      await onConfirm()
      onOpenChange(false)
    } catch (e) {
      setError(errorText(e, errorOverrides))
      setPending(false)
    }
  }

  return (
    <form
      className="flex flex-col gap-4 px-5 pb-5"
      onSubmit={(e) => {
        e.preventDefault()
        if (matched && !pending) void confirm()
      }}
    >
      <div className="text-sm text-muted-foreground">{description}</div>
      {confirmText !== undefined && (
        <Input
          autoFocus
          value={typed}
          onChange={(e) => setTyped(e.target.value)}
          placeholder={confirmText}
          aria-label={`输入「${confirmText}」确认`}
        />
      )}
      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      )}
      <div className="flex justify-end gap-2">
        <Button type="button" variant="ghost" onClick={() => onOpenChange(false)}>
          取消
        </Button>
        <Button type="submit" variant={destructive ? "destructive" : "default"} disabled={!matched || pending}>
          {pending ? "处理中…" : confirmLabel}
        </Button>
      </div>
    </form>
  )
}
