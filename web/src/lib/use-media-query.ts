import { useCallback, useSyncExternalStore } from "react"

// 768px 是布局断点（设计 3.4.4），与 Tailwind 的 md 一致
export const DESKTOP_QUERY = "(min-width: 768px)"

export function useMediaQuery(query: string): boolean {
  const subscribe = useCallback(
    (onChange: () => void) => {
      const list = window.matchMedia(query)
      list.addEventListener("change", onChange)
      return () => list.removeEventListener("change", onChange)
    },
    [query],
  )
  return useSyncExternalStore(subscribe, () => window.matchMedia(query).matches)
}
