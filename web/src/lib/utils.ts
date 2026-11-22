export { cn } from "cn"

// 按 Unicode 码点计数，与 protovalidate 的 max_len 一致（昵称、签名、群名称、公告）
export const codePoints = (s: string) => [...s].length

// 文件大小：1.5 KB、20 KB、3.4 MB
export function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  const units = ["KB", "MB", "GB"]
  let value = bytes / 1024
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit++
  }
  // 小于 10 保留一位小数，否则取整，避免「100.0 MB」
  return `${value < 10 ? value.toFixed(1) : Math.round(value)} ${units[unit]}`
}
