// 登录前后的跳转地址（设计 3.5）：未登录访问受保护的页面时带上原路径，登录后跳回。

// 登录后的默认页
export const HOME_PATH = "/chat"

// 只接受站内路径，防止 ?redirect=//evil.example 这类开放重定向。
// 浏览器把反斜杠当作斜杠（/\evil.example 等同于 //evil.example），解析 URL 时还会删掉制表符和换行（/<Tab>/evil.example），
// 所以反斜杠和空白字符一律拒绝；站内路径里的空格本来就会编码为 %20
export function safeRedirect(raw: string | null | undefined): string {
  if (!raw || !raw.startsWith("/") || raw.startsWith("//") || /[\\\s]/.test(raw)) return HOME_PATH
  // 跳回登录、注册页没有意义
  if (/^\/(login|register)(?:[/?#]|$)/.test(raw)) return HOME_PATH
  return raw
}

export function loginPath(location: { pathname: string; search: string; hash: string }): string {
  const target = location.pathname + location.search + location.hash
  return target === HOME_PATH ? "/login" : `/login?redirect=${encodeURIComponent(target)}`
}
