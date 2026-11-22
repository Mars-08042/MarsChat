import { useState } from "react"
import { AnimatePresence, motion } from "motion/react"
import { Globe, Laptop, LogOut, MonitorSmartphone, Smartphone } from "lucide-react"
import { toast } from "sonner"
import { ConfirmDialog } from "@/components/common/ConfirmDialog"
import { ListState } from "@/components/common/ListState"
import { Button } from "@/components/ui/button"
import { springs } from "@/lib/motion"
import { formatListTime } from "@/lib/time"
import { useNow } from "@/lib/use-countdown"
import { kickDevice, useDevices, type DeviceItem, type DevicePlatform } from "./queries"

const PLATFORM_ICON: Record<DevicePlatform, typeof Globe> = {
  web: Globe,
  desktop: Laptop,
  android: Smartphone,
  ios: Smartphone,
  unknown: MonitorSmartphone,
}

const PLATFORM_LABEL: Record<DevicePlatform, string> = {
  web: "网页",
  desktop: "桌面端",
  android: "Android",
  ios: "iOS",
  unknown: "未知平台",
}

const deviceName = (d: DeviceItem) => d.name || PLATFORM_LABEL[d.platform]

// 登录设备：每台设备一张卡片，当前设备排在最前；让其他设备下线时卡片移出（设计 3.6.8）
export function DevicesTab() {
  const devices = useDevices()
  const [kicking, setKicking] = useState<DeviceItem | null>(null)
  const list = devices.data ?? []

  return (
    <div className="flex flex-col gap-3">
      <p className="text-sm text-muted-foreground">在同一个浏览器里重新登录会替换原来的记录，不会多出一台设备。不认识的设备可以让它下线。</p>
      <ListState query={devices} empty={list.length === 0 ? "没有登录中的设备" : undefined}>
        <ul className="space-y-2">
          <AnimatePresence initial={false}>
            {list.map((d) => (
              <motion.li
                key={d.id}
                layout
                initial={{ opacity: 0, y: 8 }}
                animate={{ opacity: 1, y: 0 }}
                exit={{ opacity: 0, x: 40, transition: { duration: 0.2 } }}
                transition={springs.smooth}
              >
                <DeviceCard device={d} onKick={() => setKicking(d)} />
              </motion.li>
            ))}
          </AnimatePresence>
        </ul>
      </ListState>

      <ConfirmDialog
        open={kicking !== null}
        onOpenChange={(open) => !open && setKicking(null)}
        title={kicking ? `让「${deviceName(kicking)}」下线` : ""}
        description="这台设备会立即退出登录，之后需要重新登录才能继续使用。"
        confirmLabel="下线"
        destructive
        onConfirm={async () => {
          if (!kicking) return
          await kickDevice(kicking.id)
          toast(`已让「${deviceName(kicking)}」下线`)
        }}
      />
    </div>
  )
}

function DeviceCard({ device: d, onKick }: { device: DeviceItem; onKick: () => void }) {
  const now = useNow()
  const Icon = PLATFORM_ICON[d.platform]
  return (
    <div className="flex items-start gap-3 rounded-xl border bg-card p-3.5">
      <span className="flex size-10 shrink-0 items-center justify-center rounded-lg bg-muted text-muted-foreground">
        <Icon aria-label={PLATFORM_LABEL[d.platform]} className="size-5" />
      </span>
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <p className="truncate font-medium">{deviceName(d)}</p>
          {d.current && <span className="shrink-0 rounded bg-brand/10 px-1.5 py-px text-[11px] leading-4 font-medium text-brand-text">当前设备</span>}
        </div>
        <p className="mt-0.5 text-xs text-muted-foreground">
          {d.current ? "正在使用" : d.lastActiveAt ? `最近活跃 ${formatListTime(d.lastActiveAt, now)}` : "最近活跃时间未知"}
          {d.lastIp && (
            <>
              {" · "}
              <span className="font-mono">{d.lastIp}</span>
            </>
          )}
        </p>
        {d.createdAt && <p className="text-xs text-muted-foreground">登录于 {formatListTime(d.createdAt, now)}</p>}
      </div>
      {!d.current && (
        <Button variant="ghost" size="sm" className="shrink-0 text-destructive hover:text-destructive" onClick={onKick}>
          <LogOut />
          下线
        </Button>
      )}
    </div>
  )
}
