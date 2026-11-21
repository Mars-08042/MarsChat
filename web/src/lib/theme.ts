// 主题偏好保存在 localStorage（设计 3.4.3），默认亮色。
// index.html 里有一段同样逻辑的内联脚本，在首屏渲染前加上 dark 类；改这里时要同步修改那段脚本。

export type ThemePreference = "light" | "dark" | "system"

const STORAGE_KEY = "marschat.theme"

function systemDark(): MediaQueryList {
  return window.matchMedia("(prefers-color-scheme: dark)")
}

export function readThemePreference(): ThemePreference {
  const value = localStorage.getItem(STORAGE_KEY)
  return value === "dark" || value === "system" ? value : "light"
}

export function applyTheme(pref: ThemePreference): void {
  const dark = pref === "dark" || (pref === "system" && systemDark().matches)
  document.documentElement.classList.toggle("dark", dark)
}

export function saveThemePreference(pref: ThemePreference): void {
  localStorage.setItem(STORAGE_KEY, pref)
  applyTheme(pref)
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
