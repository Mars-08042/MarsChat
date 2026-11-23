// 通话状态（设计 3.9.5）：由 realtime/call/engine.ts 写入，通话层只读状态、调用 realtime/client.ts 的 callActions。

import { useStore } from "zustand"
import { createStore } from "zustand/vanilla"
import type { CallState } from "@/realtime/call/engine"

export const initialCallState = (): CallState => ({ call: null, notice: null })

export const callStore = createStore<CallState>()(() => initialCallState())

export function useCall<T>(selector: (s: CallState) => T): T {
  return useStore(callStore, selector)
}
