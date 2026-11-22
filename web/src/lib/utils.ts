export { cn } from "cn"

// 按 Unicode 码点计数，与 protovalidate 的 max_len 一致（昵称、签名、群名称、公告）
export const codePoints = (s: string) => [...s].length
