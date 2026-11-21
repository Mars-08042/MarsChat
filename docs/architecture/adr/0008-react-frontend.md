# ADR-0008 新前端：React + Vite 技术栈与代码位置

- 状态：已采纳（2026-11-20 开发者确认）
- 日期：2026-11-20
- 关联：[前端设计](../../design/frontend.md)、[前端计划](../../plans/active/frontend-rewrite.md)、[ADR-0004](0004-frontend-pnpm.md)（pnpm）

## 背景

旧的 Vue 3 前端已在后端重构步骤 0 中移除，后端重构计划规定前端用 React + pnpm 重写，并且“前端是否拆成独立仓库，在前端计划中决定”。项目用于求职展示，前端既要有辨识度强的视觉和交互，又要按 data-flow 的客户端约定实现可靠的同步。

## 候选方案

| 问题 | 候选 | 结论 |
|---|---|---|
| 代码位置 | 本仓库 `web/`；独立私有仓库 | 本仓库 `web/` |
| 构建 | Vite SPA；Next.js | Vite：页面都在登录后访问，不需要 SEO 和服务端渲染 |
| 基础组件 | shadcn/ui；Ant Design；MUI | shadcn/ui：源码在仓库里，样式可以完全定制 |
| 状态管理 | zustand + React Query；Redux Toolkit | zustand + React Query |
| 协议类型 | protobuf-es 由 buf 生成；手写 TS 类型 | protobuf-es |

## 决策

1. 前端代码放在本仓库 `web/`，用 pnpm 管理依赖（沿用 ADR-0004）。
2. 技术栈：Vite、React、TypeScript、Tailwind CSS、shadcn/ui、Motion、lucide-react、React Router、TanStack Query、zustand、sonner、TanStack Virtual，测试用 Vitest，代码检查用 ESLint。版本见设计第 3.1 节和 `web/package.json`。
3. 协议类型由 buf 用 `protoc-gen-es` 从 `api/` 生成到 `web/src/gen/`，生成代码提交到仓库；WebSocket 默认用 protobuf 二进制子协议。
4. 第三方组件：
   - React Bits 可以复制源码进仓库，但要保留版权声明；
   - Aceternity UI 和付费组件只参考设计，不提交源码；
   - 复制进仓库的源码登记在 `web/THIRD_PARTY_NOTICES.md`。
5. 令牌存储在 `localStorage`；消息只缓存在内存里，不使用 IndexedDB；每个标签页各自一条 WebSocket 连接。

## 原因

- **同一仓库**：proto 契约在本仓库。帧协议或接口变化时，proto、Go 生成代码、TS 生成代码、前后端实现能在同一个提交里一起修改；求职展示也只需要一个仓库。
- **shadcn/ui**：React Bits 也通过 shadcn CLI 安装，工具链统一；Ant Design、MUI 自带的中后台风格很难做出品牌感。
- **protobuf-es**：前后端的类型和编解码来自同一份 proto，WebSocket 帧不用手写编解码。
- **简单优先**：内存缓存、每个标签页一条连接、`localStorage` 存令牌，都不需要后端配合，复杂度最低。各自的代价见下节。

## 代价与风险

- CI 要同时安装 Go 和 Node；`make gen` 依赖 `web/node_modules`。
- 刷新令牌放在 `localStorage`，出现 XSS 时可能被窃取。缓解措施见设计第 3.8 节；项目公开演示前，再评估改用 HttpOnly Cookie（需要 account 配合）。
- 刷新页面后要重新加载会话和最新一页消息。
- 2026-11-20 核对依赖时发现：TypeScript 最新版是 7.0.2，但 typescript-eslint 8.73.0 只支持到 `<6.1.0`，因此锁定 TypeScript 6.0.3。Vite 官方模板已改用 TypeScript 7 加 oxlint，见设计第 10 节。

## 适用条件（何时需要重新评估）

- 需要服务端渲染或 SEO（例如公开的项目介绍页）时，重新评估构建框架。
- 前端需要被其他项目复用，或者交给独立团队维护时，重新评估是否拆成独立仓库。
- 需要离线查看历史或冷启动更快时，评估 IndexedDB。
- typescript-eslint 支持 TypeScript 7 后，或者决定改用 oxlint 时，重新评估 TypeScript 版本。
