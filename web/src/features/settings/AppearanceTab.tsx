import type { CSSProperties, ReactNode } from "react"
import { Check } from "lucide-react"
import { MarsLogo } from "@/components/common/MarsLogo"
import { BrandBackground } from "@/features/auth/BrandBackground"
import { FONT_PAIRS, LOGIN_BACKGROUNDS, useAppearance, type LoginBackground } from "@/lib/appearance"
import { THEME_OPTIONS, type ThemePreference } from "@/lib/theme"
import { cn } from "@/lib/utils"

// 外观（设计 3.6.8）：主题、字体组、登录页背景。都只保存在本机，切换即时生效
export function AppearanceTab() {
  const { theme, font, loginBackground, setTheme, setFont, setLoginBackground } = useAppearance()

  return (
    <div className="flex flex-col gap-7">
      <p className="text-sm text-muted-foreground">外观设置只保存在这台设备上，换设备后需要重新设置。</p>

      <Section title="主题" hint="登录和注册页固定深色">
        <div role="radiogroup" aria-label="主题" className="grid grid-cols-3 gap-3">
          {THEME_OPTIONS.map((t) => (
            <OptionCard
              key={t.id}
              selected={theme === t.id}
              label={t.label}
              onPick={(button) => {
                // 从被点的卡片中心开始圆形扩散；键盘操作时同样有位置
                const r = button.getBoundingClientRect()
                setTheme(t.id, { x: r.left + r.width / 2, y: r.top + r.height / 2 })
              }}
            >
              <ThemePreview pref={t.id} />
            </OptionCard>
          ))}
        </div>
      </Section>

      <Section title="字体组" hint="用于英文标题和等宽数字，中文始终用系统字体">
        <div role="radiogroup" aria-label="字体组" className="grid grid-cols-2 gap-3">
          {FONT_PAIRS.map((f) => (
            <OptionCard key={f.id} selected={font === f.id} label={f.label} detail={f.detail} onPick={() => setFont(f.id)}>
              <div className="flex h-16 flex-col justify-center gap-1 rounded-lg bg-muted/60 px-3">
                <span className="text-lg leading-tight font-semibold" style={{ fontFamily: `"${f.display}", sans-serif` }}>
                  MarsChat
                </span>
                <span className="text-xs text-muted-foreground" style={{ fontFamily: `"${f.code}", monospace` }}>
                  ID 1179 4039 6407
                </span>
              </div>
            </OptionCard>
          ))}
        </div>
      </Section>

      <Section title="登录页背景" hint="开启「减少动态效果」时显示静态渐变">
        <div className="flex flex-col gap-3 sm:flex-row">
          <div role="radiogroup" aria-label="登录页背景" className="grid flex-1 grid-cols-2 gap-3">
            {LOGIN_BACKGROUNDS.map((b) => (
              <OptionCard key={b.id} selected={loginBackground === b.id} label={b.label} onPick={() => setLoginBackground(b.id)}>
                <div aria-hidden="true" className="h-12 rounded-lg" style={BACKGROUND_SWATCH[b.id]} />
              </OptionCard>
            ))}
          </div>
          {/* 只给选中的背景做实时预览：同时跑四个 WebGL 画布太耗资源 */}
          <div className="relative isolate flex aspect-[4/3] items-center justify-center overflow-hidden rounded-xl sm:w-56">
            <BrandBackground kind={loginBackground} />
            <div className="flex items-center gap-2 text-white">
              <MarsLogo className="size-7" />
              <span className="font-display text-lg font-semibold">MarsChat</span>
            </div>
          </div>
        </div>
      </Section>
    </div>
  )
}

function Section({ title, hint, children }: { title: string; hint?: string; children: ReactNode }) {
  return (
    <section className="flex flex-col gap-3">
      <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
        <h2 className="text-sm font-medium">{title}</h2>
        {hint && <span className="text-xs text-muted-foreground">{hint}</span>}
      </div>
      {children}
    </section>
  )
}

function OptionCard({
  selected,
  label,
  detail,
  onPick,
  children,
}: {
  selected: boolean
  label: string
  detail?: string
  onPick: (button: HTMLButtonElement) => void
  children: ReactNode
}) {
  return (
    <button
      type="button"
      role="radio"
      aria-checked={selected}
      onClick={(e) => onPick(e.currentTarget)}
      className={cn(
        "flex flex-col gap-2 rounded-xl border bg-card p-2 text-left transition-colors outline-none focus-visible:ring-2 focus-visible:ring-ring",
        selected ? "border-brand-strong/70 ring-1 ring-brand-strong/40" : "hover:bg-muted/40",
      )}
    >
      {children}
      <span className="flex items-center justify-between gap-2 px-1 pb-0.5">
        <span className="min-w-0">
          <span className="block truncate text-sm font-medium">{label}</span>
          {detail && <span className="block truncate text-xs text-muted-foreground">{detail}</span>}
        </span>
        {selected && <Check aria-hidden="true" className="size-4 shrink-0 text-brand-text" />}
      </span>
    </button>
  )
}

// 主题缩略图的配色抄自 tokens.css（背景、卡片、muted、边框、我方气泡），只作示意
const PALETTE = {
  light: { bg: "#FAF7F3", card: "#FFFFFF", muted: "#F1ECE6", border: "#E8E1DA", mine: "#C2361F" },
  dark: { bg: "#0F0B0A", card: "#1A1412", muted: "#241C19", border: "#2E2420", mine: "#C2361F" },
}

function ThemePreview({ pref }: { pref: ThemePreference }) {
  if (pref === "system") {
    return (
      <div aria-hidden="true" className="relative h-16 overflow-hidden rounded-lg">
        <MiniChat colors={PALETTE.light} />
        <div className="absolute inset-0 [clip-path:polygon(100%_0,100%_100%,0_100%)]">
          <MiniChat colors={PALETTE.dark} />
        </div>
      </div>
    )
  }
  return (
    <div aria-hidden="true" className="h-16 overflow-hidden rounded-lg">
      <MiniChat colors={PALETTE[pref]} />
    </div>
  )
}

function MiniChat({ colors: c }: { colors: (typeof PALETTE)["light"] }) {
  return (
    <div className="flex h-full" style={{ background: c.bg, boxShadow: `inset 0 0 0 1px ${c.border}` }}>
      <div className="w-1/4" style={{ background: c.muted }} />
      <div className="flex flex-1 flex-col justify-center gap-1.5 px-2">
        <div className="h-2.5 w-3/5 rounded-full" style={{ background: c.card, boxShadow: `0 0 0 1px ${c.border}` }} />
        <div className="ml-auto h-2.5 w-1/2 rounded-full" style={{ background: c.mine }} />
        <div className="h-2.5 w-2/5 rounded-full" style={{ background: c.card, boxShadow: `0 0 0 1px ${c.border}` }} />
      </div>
    </div>
  )
}

// 背景选项的静态色块，大致对应各背景的配色（BrandBackground 里的参数），实时效果看右侧预览
const BACKGROUND_SWATCH: Record<LoginBackground, CSSProperties> = {
  grainient: { background: "linear-gradient(135deg, #0F0B0A, #3A120A 30%, #9A2A16 60%, #E2452C 85%, #FF8A66)" },
  "soft-aurora": { background: "linear-gradient(180deg, #0F0B0A 15%, #E2452C 55%, #FFB199 68%, #0F0B0A 95%)" },
  silk: { background: "linear-gradient(115deg, #2A0F09, #8C2F1B 38%, #FF8A66 52%, #8C2F1B 66%, #2A0F09)" },
  static: {
    backgroundColor: "#0F0B0A",
    backgroundImage: "radial-gradient(110% 90% at 10% 110%, rgb(226 69 44 / 0.9), transparent 62%), radial-gradient(60% 60% at 90% 0%, rgb(240 162 59 / 0.25), transparent 70%)",
  },
}
