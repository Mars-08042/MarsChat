import { useState } from "react"
import { motion } from "motion/react"
import { toast } from "sonner"
import {
  ArrowDownToLine,
  Check,
  File,
  FileArchive,
  FileCode,
  FileHeadphone,
  FileImage,
  FilePlay,
  FileSpreadsheet,
  FileText,
  ImageOff,
  LoaderCircle,
  Presentation,
  RotateCw,
  X,
  type LucideIcon,
} from "lucide-react"
import { errorText } from "@/api/errors"
import { Variant } from "@/gen/media/v1/media_pb"
import { springs } from "@/lib/motion"
import { useDownloadUrl } from "@/lib/use-download-url"
import { cn } from "@/lib/utils"
import { formatBytes } from "@/lib/utils"
import { fileCategory, fitImage, splitFileName, type FileCategory } from "./files"
import { downloadFile } from "./download"
import { ImageViewer } from "./ImageViewer"
import type { ChatMessage, FileMeta, ImageMeta } from "./types"

// 图片消息（设计 3.6.9）：按原图宽高预留占位，缩略图加载前后布局不跳动；点击用大图查看器打开
export function ImageAttachment({
  message,
  image,
  senderName,
  onCancel,
}: {
  message: ChatMessage
  image: ImageMeta
  senderName: string
  // 时间和发送状态，叠在图片右下角
  onCancel: () => void
}) {
  const [open, setOpen] = useState(false)
  const [loaded, setLoaded] = useState(false)
  const size = fitImage(image.width, image.height)
  const uploading = message.status === "uploading"
  // 自己发的图片一直用本地地址（上传期间和上传后都是），不再下载一遍；其余换取缩略图的下载地址
  const thumb = useDownloadUrl(image.fileId || undefined, Variant.THUMBNAIL, image.accessKey, !message.localUrl)
  const src = message.localUrl ?? thumb.url
  const layoutId = `image-${message.id}`

  if (!src && (thumb.missing || thumb.failed)) {
    return (
      <div className="flex flex-col items-center justify-center gap-1.5 rounded-[inherit] bg-muted text-xs text-muted-foreground" style={size}>
        <ImageOff className="size-5" />
        {thumb.missing ? (
          "图片已失效"
        ) : (
          <button type="button" onClick={thumb.retry} className="flex items-center gap-1 rounded underline-offset-2 outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring">
            <RotateCw className="size-3" />
            加载失败，点击重试
          </button>
        )}
      </div>
    )
  }

  return (
    <div className="relative overflow-hidden rounded-[inherit] bg-muted" style={size}>
      {!loaded && <div className="absolute inset-0 animate-pulse bg-muted motion-reduce:animate-none" />}
      {src && (
        <button
          type="button"
          onClick={() => setOpen(true)}
          disabled={uploading}
          aria-label={`查看图片 ${image.name}`}
          className="block size-full cursor-zoom-in outline-none focus-visible:ring-3 focus-visible:ring-ring/60 focus-visible:ring-inset disabled:cursor-default"
        >
          <motion.img
            layoutId={layoutId}
            transition={springs.smooth}
            src={src}
            alt={image.name}
            draggable={false}
            onLoad={() => setLoaded(true)}
            // 下载地址过期或签名失效时重新换取一次
            onError={message.localUrl ? undefined : thumb.onError}
            className={cn("size-full object-cover transition-opacity duration-300", loaded ? "opacity-100" : "opacity-0")}
          />
        </button>
      )}
      {uploading && <UploadRing progress={message.upload?.progress ?? 0} onCancel={onCancel} />}
      {src && (
        <ImageViewer open={open} onOpenChange={setOpen} layoutId={layoutId} src={src} localUrl={message.localUrl} image={image} senderName={senderName} />
      )}
    </div>
  )
}

// 圆形进度环，中间是取消按钮
function UploadRing({ progress, onCancel }: { progress: number; onCancel: () => void }) {
  const radius = 20
  const circumference = 2 * Math.PI * radius
  return (
    <div className="absolute inset-0 flex flex-col items-center justify-center gap-1.5 bg-black/35">
      <div className="relative size-12">
        <svg viewBox="0 0 48 48" className="size-12 -rotate-90" aria-hidden="true">
          <circle cx="24" cy="24" r={radius} fill="rgb(0 0 0 / 0.35)" stroke="rgb(255 255 255 / 0.25)" strokeWidth="3" />
          <motion.circle
            cx="24"
            cy="24"
            r={radius}
            fill="none"
            stroke="white"
            strokeWidth="3"
            strokeLinecap="round"
            strokeDasharray={circumference}
            animate={{ strokeDashoffset: circumference * (1 - progress) }}
            transition={springs.smooth}
          />
        </svg>
        <button
          type="button"
          onClick={onCancel}
          aria-label="取消上传"
          className="absolute inset-0 flex items-center justify-center rounded-full text-white outline-none focus-visible:ring-2 focus-visible:ring-white"
        >
          <X className="size-5" />
        </button>
      </div>
      <span className="font-mono text-xs text-white tabular-nums" aria-live="polite">
        {Math.round(progress * 100)}%
      </span>
    </div>
  )
}

const CATEGORY_ICON: Record<FileCategory, LucideIcon> = {
  image: FileImage,
  video: FilePlay,
  audio: FileHeadphone,
  archive: FileArchive,
  pdf: FileText,
  doc: FileText,
  sheet: FileSpreadsheet,
  slide: Presentation,
  code: FileCode,
  text: FileText,
  other: File,
}

// 文件消息（设计 3.6.9）：类型图标、文件名（截断主名、保留扩展名）、大小；上传中显示进度条和取消按钮
export function FileAttachment({
  message,
  file,
  mine,
  onCancel,
}: {
  message: ChatMessage
  file: FileMeta
  mine: boolean
  onCancel: () => void
}) {
  const Icon = CATEGORY_ICON[fileCategory(file.name, file.mimeType)]
  const [base, ext] = splitFileName(file.name)
  const uploading = message.status === "uploading"
  const progress = message.upload?.progress ?? 0

  return (
    <div className="w-[17.5rem] max-w-full">
      <div className="flex items-center gap-3">
        <span
          className={cn(
            "flex size-11 shrink-0 items-center justify-center rounded-xl",
            mine ? "bg-white/15 text-white" : "bg-card text-brand-text shadow-sm",
          )}
        >
          <Icon className="size-5" />
        </span>
        <div className="min-w-0 flex-1">
          <p className="flex min-w-0 text-sm leading-5 font-medium" title={file.name}>
            <span className="truncate">{base}</span>
            {ext && <span className="shrink-0">.{ext}</span>}
          </p>
          <p className={cn("mt-0.5 font-mono text-xs tabular-nums", mine ? "text-white/90" : "text-muted-foreground")}>
            {uploading ? `${formatBytes(Math.round(file.size * progress))} / ${formatBytes(file.size)}` : formatBytes(file.size)}
          </p>
        </div>
        {uploading ? (
          <button
            type="button"
            onClick={onCancel}
            aria-label="取消上传"
            className={cn(
              "flex size-9 shrink-0 items-center justify-center rounded-full transition-colors outline-none focus-visible:ring-2",
              mine ? "hover:bg-white/15 focus-visible:ring-white" : "hover:bg-card focus-visible:ring-ring",
            )}
          >
            <X className="size-4" />
          </button>
        ) : (
          <DownloadButton mine={mine} file={file} />
        )}
      </div>
      {uploading && (
        <div className={cn("mt-2.5 h-1 overflow-hidden rounded-full", mine ? "bg-white/20" : "bg-border")}>
          <motion.div
            className={cn("h-full origin-left rounded-full", mine ? "bg-white" : "bg-brand")}
            initial={false}
            animate={{ scaleX: progress }}
            transition={springs.smooth}
          />
        </div>
      )}
    </div>
  )
}

// 点击后先换取下载地址再下载（10 分钟有效，剩余不到 1 分钟的不复用）。上传失败的文件没有文件 ID，不能下载
function DownloadButton({ mine, file }: { mine: boolean; file: FileMeta }) {
  const [state, setState] = useState<"idle" | "resolving" | "done">("idle")
  const disabled = !file.fileId

  const download = async () => {
    setState("resolving")
    try {
      if (!(await downloadFile(file))) {
        toast("文件已失效，无法下载")
        setState("idle")
        return
      }
      setState("done")
      setTimeout(() => setState("idle"), 1500)
    } catch (e) {
      toast(errorText(e))
      setState("idle")
    }
  }

  return (
    <button
      type="button"
      onClick={() => void download()}
      disabled={disabled || state !== "idle"}
      aria-label={state === "resolving" ? "正在获取下载地址" : state === "done" ? "已开始下载" : "下载"}
      className={cn(
        "flex size-9 shrink-0 items-center justify-center rounded-full transition-colors outline-none focus-visible:ring-2 disabled:opacity-100",
        mine ? "hover:bg-white/15 focus-visible:ring-white" : "text-brand-text hover:bg-card focus-visible:ring-ring",
        disabled && "opacity-50",
      )}
    >
      <motion.span key={state} initial={{ scale: 0.5, opacity: 0 }} animate={{ scale: 1, opacity: 1 }} transition={springs.snappy}>
        {state === "resolving" ? (
          <LoaderCircle className="size-4 animate-spin motion-reduce:animate-none" />
        ) : state === "done" ? (
          <Check className="size-4" strokeWidth={2.75} />
        ) : (
          <ArrowDownToLine className="size-4" />
        )}
      </motion.span>
    </button>
  )
}
