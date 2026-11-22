// 跨页面的界面状态：会话资料面板、庆祝动效。和业务数据无关，切换账号时不需要清理。

import { create } from "zustand"

export type PanelTab = "info" | "members" | "requests"

interface UiState {
  // 会话右侧的资料面板（群资料、成员、入群申请；单聊为对方资料）。是界面状态，不占路由（设计 3.5）
  panel: { conversationId: string; tab: PanelTab } | null
  openPanel: (conversationId: string, tab?: PanelTab) => void
  closePanel: () => void
  // 每次加一播放一次庆祝动效（成为好友、建群成功，设计 3.6.7）
  celebration: number
  celebrate: () => void
  // 路由跳转，由主框架注册；实时客户端里的通知提示按钮用它跳到对应页面
  navigate: ((to: string) => void) | null
}

export const useUi = create<UiState>()((set) => ({
  panel: null,
  openPanel: (conversationId, tab = "info") => set({ panel: { conversationId, tab } }),
  closePanel: () => set({ panel: null }),
  celebration: 0,
  celebrate: () => set((s) => ({ celebration: s.celebration + 1 })),
  navigate: null,
}))
