// 组件测试的最小工具：在 jsdom 里挂载组件、派发事件。只用 React 自带的 act 和 createRoot，
// 不引入 @testing-library。测试文件开头要写 // @vitest-environment jsdom。

import { act, type ReactNode } from "react"
import { createRoot } from "react-dom/client"

// 让 React 知道当前在测试环境里，act 之外的更新会给出警告
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

export interface Mounted {
  container: HTMLElement
  // 用新的属性重新渲染同一棵树，组件状态保留
  rerender: (ui: ReactNode) => void
  unmount: () => void
}

export function mount(ui: ReactNode): Mounted {
  const container = document.createElement("div")
  document.body.appendChild(container)
  const root = createRoot(container)
  act(() => root.render(ui))
  return {
    container,
    rerender: (next) => act(() => root.render(next)),
    unmount: () => {
      act(() => root.unmount())
      container.remove()
    },
  }
}

export function fire(target: Element, event: Event): void {
  act(() => {
    target.dispatchEvent(event)
  })
}

// 受控输入框要通过原型上的 value setter 赋值，React 才会在 input 事件里读到新值
export function setValue(el: HTMLTextAreaElement | HTMLInputElement, value: string): void {
  const proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype
  Object.getOwnPropertyDescriptor(proto, "value")?.set?.call(el, value)
  fire(el, new Event("input", { bubbles: true }))
}

// jsdom 没有 matchMedia；所有媒体查询都返回同一个结果
export function stubMatchMedia(matches: boolean): void {
  window.matchMedia = (query: string) =>
    ({
      matches,
      media: query,
      onchange: null,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {},
      removeListener: () => {},
      dispatchEvent: () => false,
    }) as MediaQueryList
}
