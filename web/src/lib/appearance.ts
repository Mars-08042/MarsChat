import { create } from "zustand"
import { readThemePreference, switchTheme, type ThemePreference } from "./theme"

// 外观偏好（设计 3.6.8）：主题、字体组和登录页背景，用户在设置里切换。
// 后端没有偏好接口，只保存在本机 localStorage。主题的读写与切换动效见 theme.ts

// 新增字体组：tokens.css 加一个 :root[data-font="<id>"] 块，再在这里登记（设计 3.4.3）
// display、code 与 tokens.css 中这一组的 --display-font、--code-font 相同，设置页的预览直接用它们
export const FONT_PAIRS = [
  { id: "geist", label: "Geist", detail: "Geist + Geist Mono", display: "Geist Variable", code: "Geist Mono Variable" },
  {
    id: "grotesk",
    label: "Space Grotesk",
    detail: "Space Grotesk + JetBrains Mono",
    display: "Space Grotesk Variable",
    code: "JetBrains Mono Variable",
  },
] as const
export type FontPair = (typeof FONT_PAIRS)[number]["id"]
export const DEFAULT_FONT: FontPair = "geist"

export const LOGIN_BACKGROUNDS = [
  { id: "grainient", label: "Grainient" },
  { id: "soft-aurora", label: "Soft Aurora" },
  { id: "silk", label: "Silk" },
  { id: "static", label: "静态" },
] as const
export type LoginBackground = (typeof LOGIN_BACKGROUNDS)[number]["id"]
export const DEFAULT_LOGIN_BACKGROUND: LoginBackground = "grainient"

// 与 index.html 首屏脚本读取的键保持一致
const FONT_KEY = "marschat.font"
const BACKGROUND_KEY = "marschat.login-background"

function readChoice<T extends string>(key: string, options: readonly { id: T }[], fallback: T): T {
  try {
    const value = localStorage.getItem(key)
    return options.find((o) => o.id === value)?.id ?? fallback
  } catch {
    return fallback
  }
}

function save(key: string, value: string) {
  try {
    localStorage.setItem(key, value)
  } catch {
    // 写不进去时只在本次打开期间生效
  }
}

// 默认字体组不写属性，tokens.css 的 :root 就是默认值
export function applyFont(font: FontPair) {
  if (font === DEFAULT_FONT) delete document.documentElement.dataset.font
  else document.documentElement.dataset.font = font
}

interface AppearanceStore {
  theme: ThemePreference
  font: FontPair
  loginBackground: LoginBackground
  // from 是点击位置，主题从这里圆形扩散
  setTheme: (theme: ThemePreference, from?: { x: number; y: number }) => void
  setFont: (font: FontPair) => void
  setLoginBackground: (background: LoginBackground) => void
}

export const useAppearance = create<AppearanceStore>()((set) => ({
  theme: readThemePreference(),
  font: readChoice(FONT_KEY, FONT_PAIRS, DEFAULT_FONT),
  loginBackground: readChoice(BACKGROUND_KEY, LOGIN_BACKGROUNDS, DEFAULT_LOGIN_BACKGROUND),
  setTheme: (theme, from) => {
    switchTheme(theme, from)
    set({ theme })
  },
  setFont: (font) => {
    save(FONT_KEY, font)
    applyFont(font)
    set({ font })
  },
  setLoginBackground: (loginBackground) => {
    save(BACKGROUND_KEY, loginBackground)
    set({ loginBackground })
  },
}))
