// 只做基本格式检查，用来决定能否点「发送验证码」和提交；完整校验以后端 protovalidate 的 email 规则为准
const EMAIL_PATTERN = /^[^\s@]+@[^\s@]+\.[^\s@]+$/

export function looksLikeEmail(email: string): boolean {
  return email.length <= 254 && EMAIL_PATTERN.test(email)
}
