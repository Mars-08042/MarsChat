import { useEffect } from "react"
import { useRouteError } from "react-router"
import { Button } from "@/components/ui/button"

// 路由出错时的整页提示。按路由拆包后最常见的是页面代码下载失败：网络中断，
// 或者发布新版本后旧的带哈希文件已不存在，刷新页面会拿到新的入口。
export function RouteError() {
  const error = useRouteError()
  useEffect(() => {
    console.error("[router] route error", error)
  }, [error])

  return (
    <div role="alert" className="flex h-svh flex-col items-center justify-center gap-4 bg-background px-6 text-center text-foreground">
      <p className="font-display text-lg font-semibold">页面加载失败</p>
      <p className="max-w-sm text-sm text-muted-foreground">可能是网络中断，或者 MarsChat 刚刚更新了版本。刷新页面后重试。</p>
      <Button onClick={() => window.location.reload()}>刷新页面</Button>
    </div>
  )
}
