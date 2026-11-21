import { Construction } from "lucide-react"

// 视觉原型阶段尚未实现的页面占位
export function ComingSoon({ title, stage }: { title: string; stage: number }) {
  return (
    <div className="flex flex-1 flex-col items-center justify-center gap-3 p-8 text-center">
      <Construction className="size-8 text-brand" />
      <h1 className="font-display text-xl font-semibold">{title}</h1>
      <p className="text-sm text-muted-foreground">计划在第 {stage} 阶段实现</p>
    </div>
  )
}
