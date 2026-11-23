import type { ReactNode } from "react"
import { errorText } from "@/api/errors"
import { Button } from "@/components/ui/button"

interface ListStateProps {
  query: { isPending: boolean; isError: boolean; error: unknown; refetch: () => unknown }
  // 数据为空时的说明；为 undefined 表示不为空
  empty?: ReactNode
  children: ReactNode
}

// 列表页的加载中、失败（可重试，设计 3.6.6）、空状态；都不是时渲染 children
export function ListState({ query, empty, children }: ListStateProps) {
  if (query.isPending) return <p className="py-12 text-center text-sm text-muted-foreground">正在加载…</p>
  if (query.isError) {
    return (
      <div className="flex flex-col items-center gap-3 py-12 text-sm text-muted-foreground">
        <p>{errorText(query.error)}</p>
        <Button variant="outline" size="sm" onClick={() => void query.refetch()}>
          重试
        </Button>
      </div>
    )
  }
  if (empty !== undefined) return <div className="py-12 text-center text-sm text-muted-foreground">{empty}</div>
  return <>{children}</>
}
