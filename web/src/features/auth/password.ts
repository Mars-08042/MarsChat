// 注册密码规则，与 RegisterRequest.password 的 protovalidate 规则（^[!-~]{8,64}$）
// 和后端 WEAK_PASSWORD 校验（同时包含字母和数字）保持一致。

export interface PasswordRule {
  id: "length" | "letter" | "digit" | "charset"
  label: string
  test: (password: string) => boolean
}

export const PASSWORD_RULES: readonly PasswordRule[] = [
  { id: "length", label: "8～64 位", test: (p) => p.length >= 8 && p.length <= 64 },
  { id: "letter", label: "包含字母", test: (p) => /[A-Za-z]/.test(p) },
  { id: "digit", label: "包含数字", test: (p) => /[0-9]/.test(p) },
  // 可打印 ASCII 不含空格；空密码不算违反，避免一打开页面就显示错误
  { id: "charset", label: "不含空格和中文", test: (p) => /^[!-~]*$/.test(p) },
]

export function isValidPassword(password: string): boolean {
  return PASSWORD_RULES.every((rule) => rule.test(password))
}
