import { useEffect, useState } from "react"
import { Dialog } from "@base-ui/react/dialog"
import { AnimatePresence, motion } from "motion/react"
import { ArrowDownToLine, LoaderCircle, X } from "lucide-react"
import { toast } from "sonner"
import { errorText } from "@/api/errors"
import { Variant } from "@/gen/media/v1/media_pb"
import { springs } from "@/lib/motion"
import { useDownloadUrl } from "@/lib/use-download-url"
import { formatBytes } from "@/lib/utils"
import { downloadFile, saveAs } from "./download"
import type { ImageMeta } from "./types"

interface ImageViewerProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  // 与缩略图共用的 layoutId：打开时图片从气泡里的位置放大到全屏，关闭时缩回去
  layoutId: string
  // 气泡里已经显示的图片（缩略图或本地地址），打开时先显示它
  src: string
  // 自己发的图片的本地地址，它就是原图，不用再下载
  localUrl?: string
  image: ImageMeta
  senderName: string
}

// 大图查看器（设计 3.6.9）。Base UI Dialog 负责焦点约束、Esc 关闭、滚动锁定；
// 弹层用 AnimatePresence 控制挂载，Portal 加 keepMounted，退出动画播完才卸载（base-ui.com 动画手册）。
export function ImageViewer({ open, onOpenChange, layoutId, src, localUrl, image, senderName }: ImageViewerProps) {
  const original = useDownloadUrl(image.fileId || undefined, Variant.ORIGINAL, image.accessKey, open && !localUrl)
  const full = useLoadedImage(localUrl ?? original.url)
  const [downloading, setDownloading] = useState(false)

  const download = async () => {
    if (localUrl) {
      saveAs(localUrl, image.name)
      return
    }
    setDownloading(true)
    try {
      if (!(await downloadFile(image))) toast("图片已失效，无法下载")
    } catch (e) {
      toast(errorText(e))
    } finally {
      setDownloading(false)
    }
  }

  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <AnimatePresence>
        {open && (
          <Dialog.Portal keepMounted>
            <Dialog.Backdrop
              render={
                <motion.div
                  initial={{ opacity: 0 }}
                  animate={{ opacity: 1 }}
                  exit={{ opacity: 0 }}
                  transition={springs.gentle}
                  className="fixed inset-0 z-50 bg-[#0F0B0A]/90 backdrop-blur-md"
                />
              }
            />
            <Dialog.Popup className="fixed inset-0 z-50 flex flex-col outline-none">
              <motion.header
                initial={{ opacity: 0, y: -8 }}
                animate={{ opacity: 1, y: 0 }}
                exit={{ opacity: 0, y: -8 }}
                transition={springs.gentle}
                className="flex items-center gap-3 px-5 py-4 text-[#F5EFE9]"
              >
                <div className="min-w-0">
                  <Dialog.Title className="truncate text-sm font-medium">{image.name}</Dialog.Title>
                  <p className="mt-0.5 font-mono text-xs text-[#A89C94]">
                    {senderName} · {image.width} × {image.height} · {formatBytes(image.size)}
                  </p>
                </div>
                <div className="ml-auto flex items-center gap-1">
                  <button
                    type="button"
                    onClick={() => void download()}
                    disabled={downloading}
                    aria-label="下载原图"
                    className="flex size-10 items-center justify-center rounded-full transition-colors outline-none hover:bg-white/10 focus-visible:ring-2 focus-visible:ring-[#FF6A4D]"
                  >
                    {downloading ? <LoaderCircle className="size-5 animate-spin motion-reduce:animate-none" /> : <ArrowDownToLine className="size-5" />}
                  </button>
                  <Dialog.Close
                    aria-label="关闭"
                    className="flex size-10 items-center justify-center rounded-full transition-colors outline-none hover:bg-white/10 focus-visible:ring-2 focus-visible:ring-[#FF6A4D]"
                  >
                    <X className="size-5" />
                  </Dialog.Close>
                </div>
              </motion.header>
              {/* 点图片以外的区域关闭 */}
              <div className="flex min-h-0 flex-1 items-center justify-center p-4 pb-10" onClick={() => onOpenChange(false)}>
                <motion.img
                  layoutId={layoutId}
                  transition={springs.smooth}
                  // 原图下载完成后再替换，之前一直显示已加载的缩略图
                  src={full ?? src}
                  alt={image.name}
                  draggable={false}
                  onClick={(e) => e.stopPropagation()}
                  className="max-h-full max-w-full rounded-lg object-contain shadow-[0_30px_80px_rgb(0_0_0/0.5)]"
                  style={{ aspectRatio: `${image.width} / ${image.height}` }}
                />
              </div>
            </Dialog.Popup>
          </Dialog.Portal>
        )}
      </AnimatePresence>
    </Dialog.Root>
  )
}

// 在后台加载图片，加载完成后返回地址；加载失败时返回 undefined，继续显示缩略图
function useLoadedImage(url: string | undefined): string | undefined {
  const [loaded, setLoaded] = useState<string>()
  useEffect(() => {
    if (!url) return
    let cancelled = false
    const img = new Image()
    img.onload = () => !cancelled && setLoaded(url)
    img.src = url
    return () => {
      cancelled = true
    }
  }, [url])
  return url && loaded === url ? url : undefined
}
