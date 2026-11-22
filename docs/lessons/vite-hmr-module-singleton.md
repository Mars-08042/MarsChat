# 持有单例的模块被热替换后，界面动作静默失效

- 涉及：[web/src/realtime/client.ts](../../web/src/realtime/client.ts)（模块级的 `current` 连接实例）、[web/src/app/AppShell.tsx](../../web/src/app/AppShell.tsx)（`useRealtime`）、设计 [frontend.md](../design/frontend.md) 第 10 节第 53 条
- 发现时间：2026-11-22，前端第 5 阶段联调（只影响开发环境）

## 现象

联调中改过 `realtime/client.ts` 后，没刷新的标签页陆续出现说不通的问题：

- 打开一个单聊，时间线一直停在「正在加载消息…」，连接胶囊却显示「已连接」；
- 退群后会话从列表消失了，但页面没有回到会话列表；
- 通知提示上的按钮点了没有反应（更早的一次，当时用 `import("@/app/router")` 跳转）。

刷新页面后一切正常。

## 根因

1. `client.ts` 在模块作用域里保存当前连接实例（`let current`），界面调用的 `chat.open`、`chat.removeConversation` 都通过它工作；实例由 `AppShell` 的 `useRealtime` 在登录后调用 `startRealtime` 创建。
2. 改动 `client.ts` 时，它不是热替换边界，更新沿导入链传到 React 组件（React Refresh 边界）。组件重新导入，拿到的是**新的** `client.ts` 模块实例，新模块里的 `current` 是空的。
3. `AppShell` 的 effect 依赖没变，不会重新执行，`startRealtime` 不会在新模块里再调用一次。旧模块里的连接照常收推送、写 store，所以连接胶囊显示正常；而组件调用的是新模块的 `chat.*`，`current` 为空，什么也不做。
4. 动态 `import()` 也一样：拿到的可能是热替换后的另一份模块实例。

## 排查要点

- 「刷新就好」「状态显示正常但动作无效」，先怀疑热替换造成的新旧模块并存，而不是业务逻辑。
- 看开发服务器日志：改动后的 `hmr update` 列出的是组件文件，说明更新越过了持有状态的模块。
- 联调验证前，改过持有单例的模块，就先整页刷新所有测试标签页，避免把热替换的副作用当成缺陷。

## 解决

- `client.ts` 自己接受更新，并在回调里整页刷新：`if (import.meta.hot) import.meta.hot.accept(() => location.reload())`。它依赖的同步引擎、连接状态机改动时，更新也会传到这里，同样整页刷新。验证：改动后开发服务器日志的 `hmr update` 只列出 `client.ts`，页面的 `performance.timeOrigin` 变为改动时刻。
- 不能只调用 `import.meta.hot.invalidate()`：它把更新继续传给导入方，组件照样接受热替换，问题不变（[Vite HMR API](https://vite.dev/guide/api-hmr)）。
- 实时模块需要的路由跳转，由主框架把 `navigate` 注册到 `stores/ui.ts`，不在实时模块里动态导入路由。
