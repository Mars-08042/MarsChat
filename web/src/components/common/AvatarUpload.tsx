import { useEffect, useRef, useState } from "react"
import { Camera, LoaderCircle } from "lucide-react"
import { toast } from "sonner"
import { uploadFile } from "@/api/client"
import { uploadErrorText } from "@/api/errors"
import { Button } from "@/components/ui/button"
import { checkAvatar, MAX_AVATAR_BYTES } from "@/features/chat/files"
import { Purpose } from "@/gen/media/v1/media_pb"
import { cn, formatBytes } from "@/lib/utils"
import { UserAvatar } from "./UserAvatar"

const ACCEPT = "image/jpeg,image/png,image/gif,image/webp"

interface AvatarUploadProps {
  seed: string
  name: string
  avatarId?: string
  shape?: "circle" | "square"
  // 上传成功后把文件 ID 写入资料（UpdateMe 或修改群资料）；传空字符串表示移除头像
  onChange: (fileId: string) => Promise<void>
  label?: string
  // 只显示头像和相机按钮，不显示说明和「移除」（群面板顶部）
  compact?: boolean
}

// 头像上传（设计 3.6.9）：先检查类型和 5 MiB 上限，上传时用本地预览盖上进度，成功后立即写入资料，
// 不和资料表单的「保存」绑在一起。上传成功后继续显示本地预览，避免换成下载地址时闪一下首字
export function AvatarUpload({ seed, name, avatarId, shape = "circle", onChange, label = "头像", compact = false }: AvatarUploadProps) {
  const input = useRef<HTMLInputElement>(null)
  const [preview, setPreview] = useState<string>()
  const [progress, setProgress] = useState<number>()
  const [removing, setRemoving] = useState(false)
  const busy = progress !== undefined || removing

  // 卸载或换了预览时释放上一个本地地址
  useEffect(() => {
    if (!preview) return
    return () => URL.revokeObjectURL(preview)
  }, [preview])

  const pick = async (file: File) => {
    const problem = checkAvatar(file)
    if (problem) {
      toast(problem)
      return
    }
    setPreview(URL.createObjectURL(file))
    setProgress(0)
    try {
      const info = await uploadFile(file, {
        name: file.name,
        purpose: Purpose.AVATAR,
        onProgress: (loaded, total) => setProgress(total > 0 ? loaded / total : 0),
      }).promise
      await onChange(info.id.toString())
      toast(`${label}已更新`)
    } catch (e) {
      setPreview(undefined)
      toast(uploadErrorText(e))
    } finally {
      setProgress(undefined)
    }
  }

  const remove = async () => {
    setRemoving(true)
    try {
      await onChange("")
      setPreview(undefined)
      toast(`已移除${label}`)
    } catch (e) {
      toast(uploadErrorText(e))
    } finally {
      setRemoving(false)
    }
  }

  return (
    <div className="flex items-center gap-4">
      <div className="relative shrink-0">
        <UserAvatar seed={seed} name={name} avatarId={avatarId} src={preview} shape={shape} size="xl" />
        {progress !== undefined && (
          <div className={cn("absolute inset-0 flex items-center justify-center bg-black/45 text-white", shape === "square" ? "rounded-[30%]" : "rounded-full")}>
            <span className="font-mono text-xs tabular-nums" aria-live="polite">
              {Math.round(progress * 100)}%
            </span>
          </div>
        )}
        <button
          type="button"
          disabled={busy}
          onClick={() => input.current?.click()}
          aria-label={`更换${label}`}
          className="absolute -right-1 -bottom-1 flex size-7 items-center justify-center rounded-full border bg-card text-foreground shadow-sm transition-colors outline-none hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-60"
        >
          {busy ? <LoaderCircle className="size-3.5 animate-spin motion-reduce:animate-none" /> : <Camera className="size-3.5" />}
        </button>
        <input
          ref={input}
          type="file"
          accept={ACCEPT}
          hidden
          onChange={(e) => {
            const file = e.target.files?.[0]
            // 清空后同一个文件可以再选一次
            e.target.value = ""
            if (file) void pick(file)
          }}
        />
      </div>
      {!compact && (
        <div className="min-w-0 text-sm">
          <p className="text-muted-foreground">
            JPEG、PNG、GIF 或 WebP 图片，不超过 {formatBytes(MAX_AVATAR_BYTES)}。没有{label}时显示首字。
          </p>
          {(avatarId || preview) && (
            <Button type="button" variant="link" size="xs" className="mt-1 h-auto px-0 text-muted-foreground" disabled={busy} onClick={() => void remove()}>
              移除{label}
            </Button>
          )}
        </div>
      )}
    </div>
  )
}
