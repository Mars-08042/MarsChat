// 主题偏好保存在 localStorage（设计 3.4.3），默认亮色。
// index.html 里有一段同样逻辑的内联脚本，在首屏渲染前加上 dark 类；改这里时要同步修改那段脚本。

export type ThemePreference = "light" | "dark" | "system"

export const THEME_OPTIONS: readonly { id: ThemePreference; label: string }[] = [
  { id: "light", label: "亮色" },
  { id: "dark", label: "深色" },
  { id: "system", label: "跟随系统" },
]

const STORAGE_KEY = "marschat.theme"

function systemDark(): MediaQueryList {
  return window.matchMedia("(prefers-color-scheme: dark)")
}

const isDark = (pref: ThemePreference) => pref === "dark" || (pref === "system" && systemDark().matches)

export function readThemePreference(): ThemePreference {
  try {
    const value = localStorage.getItem(STORAGE_KEY)
    return value === "dark" || value === "system" ? value : "light"
  } catch {
    return "light"
  }
}

export function applyTheme(pref: ThemePreference): void {
  document.documentElement.classList.toggle("dark", isDark(pref))
}

export function saveThemePreference(pref: ThemePreference): void {
  try {
    localStorage.setItem(STORAGE_KEY, pref)
  } catch {
    // 写不进去时只在本次打开期间生效
  }
  applyTheme(pref)
}

// 切换主题（设计 3.4.5）：用 View Transitions 从 from（点击的位置）圆形扩散出新主题。
// 浏览器不支持、开启「减少动态效果」、没有起点，或者深浅色实际没变（例如跟随系统且系统同色）时直接切换
export function switchTheme(pref: ThemePreference, from?: { x: number; y: number }): void {
  const changes = isDark(pref) !== document.documentElement.classList.contains("dark")
  const reduceMotion = window.matchMedia("(prefers-reduced-motion: reduce)").matches
  if (!changes || reduceMotion || !from || !("startViewTransition" in document)) {
    saveThemePreference(pref)
    return
  }
  const transition = document.startViewTransition(() => saveThemePreference(pref))
  // 扩散到离起点最远的角
  const radius = Math.hypot(Math.max(from.x, window.innerWidth - from.x), Math.max(from.y, window.innerHeight - from.y))
  transition.ready
    .then(() => {
      document.documentElement.animate(
        { clipPath: [`circle(0px at ${from.x}px ${from.y}px)`, `circle(${radius}px at ${from.x}px ${from.y}px)`] },
        { duration: 450, easing: "cubic-bezier(0.22, 1, 0.36, 1)", pseudoElement: "::view-transition-new(root)" },
      )
    })
    // 过渡被跳过（例如连续快速切换）时 ready 会 reject，主题已经由回调切换好
    .catch(() => undefined)
}

// 选择「跟随系统」时，系统切换深浅色后立即生效。返回取消监听的函数
export function watchSystemTheme(): () => void {
  const media = systemDark()
  const onChange = () => {
    if (readThemePreference() === "system") applyTheme("system")
  }
  media.addEventListener("change", onChange)
  return () => media.removeEventListener("change", onChange)
}
