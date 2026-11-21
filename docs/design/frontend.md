# 设计：React 前端与 UI

- 状态：已采纳（2026-11-20 开发者确认，第 9 节的 6 个问题均按推荐方案） | 日期：2026-11-20 | 关联：[前端计划](../plans/active/frontend-rewrite.md)、[ADR-0008](../architecture/adr/0008-react-frontend.md)（技术栈与代码位置）、[ADR-0004](../architecture/adr/0004-frontend-pnpm.md)（pnpm）、[ADR-0006](../architecture/adr/0006-message-sync-model.md)（同步模型）、[data-flow.md](../architecture/data-flow.md)（帧协议与客户端约定）、[account-service.md](account-service.md)（令牌）、[media-service.md](media-service.md)（文件）、[gateway-multi-instance.md](gateway-multi-instance.md)（网关）

## 1. 背景与目标

旧的 Vue 前端已在重构步骤 0 中移除。新后端的 account、social、message、access、delivery 已经可用；media 正在实施（步骤 6）；通话信令还没有开始（步骤 7）。后端重构计划规定前端另立计划，用 React 和 pnpm（ADR-0004）。

项目用于求职展示，前端要做到两点：

1. 视觉和交互有辨识度，看得出是认真设计过的产品，而不是组件库的默认样式。
2. 把后端的可靠性做成用户看得见的交互：发送确认、幂等重发、断线补拉、多端同步、已读回执。

目标：

- 实现 React 单页应用，覆盖后端已有的全部用户功能（2.1）。
- 建立一套设计规范：颜色、字体、形状、动效参数，包含亮色和深色两套主题（3.4）。
- 按 data-flow 第 3、5.2、7 节的客户端约定实现连接与同步引擎，行为与 `test/cluster` 测试客户端一致（3.9）。
- 优先复用现成组件，自己只写业务组件和少量定制动效（3.3）。

不做：

- 移动端原生应用、桌面客户端、PWA 离线、Web Push 推送。
- 用 IndexedDB 在本地持久化消息（5.4）。
- 后端不支持的功能：在线状态、正在输入、消息撤回、消息搜索、用户搜索。
- 国际化，只提供简体中文。
- 音视频通话，等步骤 7 完成后再追加。

## 2. 需求与约束

### 2.1 功能范围

| 模块 | 功能 | 后端 | 计划阶段 |
|---|---|---|---|
| 认证 | 邮箱验证码注册、密码登录、验证码登录、刷新令牌、退出 | account | 3 |
| 消息 | 会话列表与未读数、历史、实时收发文本、已读、单聊已读回执、多端同步、断线补拉、被踢下线 | message、access | 4 |
| 好友 | 按用户 ID 申请（带附言）、收到和发出的申请、同意、拒绝、好友列表、删除、拉黑与黑名单 | social | 5 |
| 群组 | 建群、群资料与修改、全员禁言、直接加入或申请、审批、邀请好友、成员列表、移除、设置管理员、单人禁言、转让、退群、解散 | social | 5 |
| 通知 | 处理 `Notification` 帧：实时提示并刷新对应数据 | delivery | 5 |
| 资料与设备 | 查看和修改个人资料、查看他人资料、设备列表与踢设备 | account | 6 |
| 管理 | 按用户 ID 封禁、解封 | account | 6 |
| 文件 | 头像、图片消息、文件消息 | media（步骤 6 完成后） | 7 |

### 2.2 后端带来的约束

- **加好友只能按用户 ID**：没有用户搜索接口。界面要把「我的 ID」放在显眼位置，并提供一键复制。
- **没有在线状态和输入状态接口**：不设计在线圆点，也不设计「对方正在输入」。
- **断线期间的通知不补发**（data-flow 5.4）：通知中心的数据来自好友申请和入群申请两个列表接口，`Notification` 帧只负责弹出提示和触发刷新。
- **单聊的第一条消息才会创建会话**：从好友资料发起聊天时，会话 ID 还不知道。先以对方的用户 ID 打开草稿会话，收到 `SendAck` 后换成真正的会话 ID（3.9.4）。
- **管理员只有封禁和解封接口**，没有用户列表，管理页就是一个按 ID 操作的表单。
- **发送只走 WebSocket，读取只走 HTTP**（data-flow 第 1 节）。
- **网关没有配置 CORS**：开发时用 Vite 代理，部署时前端与网关同源（3.10）。
- **入口限流按客户端 IP 计数**：经 Docker Desktop 端口转发时，本机所有请求的来源 IP 相同，所以 `/v1/auth/*` 每分钟 60 次的限流对本机相当于全局限流，开发时反复登录可能遇到 429。网关返回的响应体是 `{"error_msg": ...}`，不是 Kratos 的错误格式。
- **令牌**：访问令牌有效期 900 秒；刷新令牌 30 天，每次刷新都轮换。10 秒宽限期内的并发刷新返回 `REFRESH_TOKEN_ROTATED`，超过宽限期后重放会吊销整个会话（account-service 设计）。
- **int64 字段**在 protobuf JSON 中是字符串，在 protobuf-es 生成的类型中是 `bigint`。

### 2.3 非功能约束

- **浏览器**：最新版 Chrome、Edge、Firefox、Safari。桌面优先；窄屏（宽度小于 768px）可以正常使用，改为单栏切换。
- **性能**：
  - 按路由分包，登录页的 WebGL 背景单独懒加载；
  - 历史消息用虚拟列表渲染；
  - 构建时记录产物体积，作为后续优化的基线。
- **无障碍**：
  - 全部操作可以只用键盘完成；
  - 文字对比度满足 WCAG AA（正文 4.5:1，大字 3:1）；
  - 遵循系统的「减少动态效果」设置（`prefers-reduced-motion`）。
- **安全**：
  - 消息正文按纯文本渲染，不使用 `dangerouslySetInnerHTML`；
  - 链接只识别 `http`、`https`，并加 `rel="noopener noreferrer"`；
  - 日志和错误上报中不出现令牌。
- **许可证**：复制进仓库的第三方源码保留版权声明，见 3.3。

## 3. 方案

### 3.1 技术栈

版本为 2026-11-20 从 npmmirror（pnpm 当前配置的镜像）查到的最新版。搭建脚手架时锁定为当时的稳定版本，记录在 `package.json` 和 `pnpm-lock.yaml` 中。

| 用途 | 选型 | 版本 | 说明 |
|---|---|---|---|
| 构建 | Vite | 8.3.8 | 纯 SPA，开发代理解决同源问题 |
| 框架 | React | 19.3.1 | |
| 语言 | TypeScript | 7.0.2 | 与 typescript-eslint、Vite 插件的兼容性在搭建脚手架时验证，见第 7 节 |
| 样式 | Tailwind CSS | 4.3.5 | 设计规范写成 CSS 变量，再通过 `@theme` 暴露给 Tailwind |
| 基础组件 | shadcn/ui | CLI 4.23.0 | 源码生成进仓库，基于无障碍原语 |
| 动效 | motion | 14.14.0 | `import { motion } from "motion/react"` |
| 图标 | lucide-react | 1.77.0 | 全站唯一的图标库 |
| 路由 | react-router | 8.5.2 | |
| 服务端数据 | @tanstack/react-query | 5.108.2 | 好友、群、申请、资料、设备等 HTTP 列表 |
| 客户端状态 | zustand | 5.1.0 | 连接状态、会话与消息时间线、发送队列 |
| 消息提示 | sonner | 2.0.8 | 样式按设计规范定制 |
| 虚拟列表 | @tanstack/react-virtual | 3.14.26 | 消息时间线、长列表 |
| 协议 | @bufbuild/protobuf、@bufbuild/protoc-gen-es | 2.17.0 | 由 buf 从 `api/` 生成 TS 类型和编解码 |
| 测试 | vitest、@testing-library/react、jsdom | 5.1.1、16.3.3、31.0.1 | |
| 代码检查 | ESLint | 10.15.0 | |

英文标题字体和等宽字体在原型阶段选定（3.4.3），确定后再把对应的 `@fontsource` 包追加到依赖清单。

### 3.2 代码位置与目录

前端代码放在本仓库的 `web/` 目录，理由见 5.1。

```text
web/
├── package.json、pnpm-lock.yaml、pnpm-workspace.yaml（allowBuilds，见 ADR-0004）
├── vite.config.ts、tsconfig*.json、eslint.config.js、components.json（shadcn）
├── THIRD_PARTY_NOTICES.md      复制进仓库的第三方源码及其许可证
└── src/
    ├── gen/                    buf 生成的 TS 代码（提交到仓库，不手改）
    ├── app/                    入口、路由、全局 Provider、错误边界
    ├── api/                    HTTP 客户端、错误解析、令牌管理
    ├── realtime/               WebSocket 连接、帧编解码、同步引擎、发送队列（纯 TS，不依赖 React）
    ├── stores/                 zustand store：连接状态、会话、时间线
    ├── features/               auth、chat、contacts、groups、settings、admin，每个目录放页面、业务组件、hooks
    ├── components/
    │   ├── ui/                 shadcn 生成的基础组件
    │   ├── effects/            React Bits 等第三方动效组件（保留版权头）
    │   └── common/             头像、空状态、连接状态胶囊等通用业务组件
    ├── styles/                 tokens.css（颜色、字体、圆角、阴影、动效变量）、全局样式
    └── lib/                    时间格式化、ID 工具、链接识别等
```

`realtime/` 不依赖 React，同步、去重、补拉、重发的逻辑都可以用 vitest 直接测试，不需要浏览器。

### 3.3 组件来源与许可证

组件分三层：

- **基础交互**用 shadcn/ui；
- **动效**统一用 Motion；
- **视觉高光**少量借用 React Bits。

付费组件和不允许再分发源码的组件，只参考设计，用 Motion 自己实现。

| 来源 | 用法 | 许可证（2026-11-20 核对） | 处理方式 |
|---|---|---|---|
| shadcn/ui | 对话框、下拉菜单、标签页、弹出层、抽屉、验证码输入、滚动区、提示气泡等 | MIT | CLI 生成到 `components/ui/`，按设计规范修改样式 |
| React Bits | 登录页背景、标题文字动画、点击火花、建群向导等少量高光 | MIT + Commons Clause：可以作为应用的一部分使用和发布，必须保留版权声明，不能单独再分发组件本身 | 用 `npx shadcn@latest add @react-bits/<名称>-TS-TW` 安装到 `components/effects/`，文件头保留版权和许可证说明，同时登记到 `THIRD_PARTY_NOTICES.md` |
| Aceternity UI | 参考动画模态框、浮动 Dock、输入框提交效果的设计 | 自有许可证：禁止再分发源码，修改过也不行 | 仓库目前是私有的，但将来求职展示时可能公开，所以**不把源码提交进仓库**，只参考设计，自己实现 |
| hover.dev | 参考通知滑入、堆叠通知的设计 | 免费和付费混合；免费组件的许可证没有核对 | 只参考设计；如果要直接使用免费组件，引入前先核对许可证 |
| uiverse.io | 加载动画、开关等小件 | MIT（站点页脚说明） | 代码注释里写明作者和原始链接，登记到 `THIRD_PARTY_NOTICES.md` |
| minimal.gallery | 视觉参考，例如 Rig AI：火星红铺满首屏、近黑色大字号标题、等宽字体跑马灯 | — | 只作参考 |
| Lucide | 图标 | ISC | npm 依赖 |
| Motion | 动效 | MIT | npm 依赖 |

React Bits 的组件在选定前要核对两点：

- **依赖**：优先选用 `ogl` 实现的 WebGL 组件，避免为一个背景引入 three.js。
- **兼容性**：Tailwind 4 下能否正常工作，在原型阶段验证。

### 3.4 视觉规范

#### 3.4.1 设计原则

- **克制**：火星红是唯一的强调色，其他颜色都是暖色调的中性色。暖色比冷灰更有生命力。
- **高频区域要安静**：聊天主界面每天高频使用，动效只做位移和透明度，时长不超过 200 毫秒。
- **低频场景才炫**：炫的效果只放在登录注册页、空状态、成为好友或建群成功、首次进入这几个场景。
- **一致**：全站只用 3 组动效参数、一套圆角和阴影规则。

#### 3.4.2 颜色

对比度按 WCAG 2.x 的相对亮度公式计算。变量的权威定义在 [web/src/styles/tokens.css](../../web/src/styles/tokens.css)，`tokens.test.ts` 会逐项核对下表中的组合。变量名沿用 shadcn/ui 的约定（第 10 节第 3 条），下表是主要变量。

| 变量 | 亮色 | 深色 | 用途 |
|---|---|---|---|
| `--brand` | `#E2452C` | `#FF6A4D` | 品牌色：大色块、图标、大号标题、装饰 |
| `--brand-strong`、`--primary` | `#C2361F` | `#C2361F` | 白字按钮、我方气泡 |
| `--brand-text` | `#C2361F` | `#FF6A4D` | 普通背景上的红色文字、链接 |
| `--accent` | `#FDEBE6` | `#2A1A16` | 选中的会话、悬停背景（shadcn 的 accent 含义） |
| `--background` | `#FAF7F3`（月岩白） | `#0F0B0A`（夜色） | 页面背景 |
| `--card`、`--popover` | `#FFFFFF` | `#1A1412` | 面板、卡片、弹出层 |
| `--bubble-peer`、`--muted` | `#F1ECE6` | `#241C19` | 对方气泡、弱化背景 |
| `--foreground` | `#17110F`（墨黑） | `#F5EFE9` | 正文 |
| `--muted-foreground` | `#6E635C`（尘灰） | `#A89C94` | 次要文字、时间 |
| `--gold` | `#F0A23B`（沙金） | `#F0A23B` | 少量点缀，比如未读徽章，不跟红色抢主角；上面的文字用 `--gold-foreground`（墨黑） |
| `--destructive` | `#B42318` | `#FF8A80` | 错误 |
| `--success` | `#187A4C` | `#3FBF86` | 成功 |
| `--ring` | `#E2452C` | `#FF6A4D` | 焦点环 |

关键组合的对比度：

| 组合 | 对比度 | 结论 |
|---|---|---|
| 白字 / `#E2452C` | 4.10 | **不满足**正文要求，所以白字按钮和我方气泡改用 `--brand-strong` |
| 白字 / `#C2361F` | 5.47 | 满足 |
| `#C2361F` 字 / 月岩白 | 5.12 | 满足；`#E2452C` 字 / 月岩白只有 3.84，只能用于大字 |
| `#C2361F` 字 / `#FDEBE6` | 4.74 | 满足 |
| 墨黑字 / 月岩白 | 17.50 | 满足 |
| 尘灰字 `#6E635C` / 月岩白、白色、`#F1ECE6` | 5.46、5.83、4.96 | 满足；初稿的 `#7A6F68` 在 `#F1ECE6` 上只有 4.16，已调整（第 10 节第 2 条） |
| 墨黑字 / `#E2452C`（Rig AI 式红底黑字） | 4.56 | 满足，用于登录页和营销式大色块 |
| `#FF6A4D` / 深色面板 | 6.44 | 满足 |
| `#A89C94` / 深色背景、深色面板 | 7.32、6.81 | 满足 |
| 墨黑字 / 沙金 | 8.84 | 满足 |
| `#B42318` / 月岩白；`#FF8A80` / 深色背景 | 6.16；8.58 | 满足 |
| `#187A4C` / 月岩白；`#3FBF86` / 深色背景 | 5.01；8.40 | 满足；原候选 `#1F8A5B` 只有 4.06，已替换 |
| 焦点环 `#E2452C` / 月岩白 | 3.84 | 满足非文字元素 3:1 的要求 |

品牌色和错误色都是红色系，所以错误状态**必须配图标和文字**，不能只靠颜色区分。

#### 3.4.3 主题与字体

- **主题**：
  - 默认亮色，聊天是长时间使用的场景；
  - 登录和注册页固定用深色，配火星红光效，负责第一眼的冲击力；
  - 提供深色主题，可以选择跟随系统或手动切换，选择保存在 `localStorage`。
- **中文**：用系统字体（`"PingFang SC", "Microsoft YaHei UI", "Microsoft YaHei", "Noto Sans SC", sans-serif`）。中文 webfont 体积太大，不引入。
- **英文标题**：用一款几何无衬线字体。原型阶段对比 2 组候选后选定，自托管。
- **等宽字体**：用于用户 ID、时间戳、调试信息，呼应 Rig AI 的工程感。

#### 3.4.4 布局与形状

- **间距**：以 4px 为基准。
- **圆角**：卡片和面板 16px，按钮和输入框 10px，消息气泡 18px（同一发送者连续消息的相邻角收窄为 6px）。
- **阴影**：用带暖色调的半透明阴影，不用纯黑。
- **断点**：
  - 1280px 及以上：导航栏、列表、聊天区三栏，群资料面板固定在右侧；
  - 768～1279px：三栏，资料面板改为抽屉；
  - 768px 以下：单栏，底部标签栏，进入会话时整屏切换。

#### 3.4.5 动效

| 名称 | 参数（初值，原型阶段调整） | 用途 |
|---|---|---|
| `snappy` | spring，stiffness 500、damping 32 | 按钮按下、徽章计数、勾选 |
| `smooth` | spring，stiffness 260、damping 28 | 列表重排、面板和抽屉、气泡入场 |
| `gentle` | 时长 0.35 秒，缓出曲线 | 页面切换、登录页元素依次出现 |

规则：

- 用 `MotionConfig` 设置全站默认参数，并设置 `reducedMotion="user"`：系统开启「减少动态效果」时，位移和 layout 动画关闭，透明度和颜色动画保留（motion.dev 文档核对）。
- 高频区域只对 `transform` 和 `opacity` 做动画，不对宽高、阴影做动画。
- WebGL 背景只用于登录注册页：
  - 标签页不可见时暂停；
  - 限制设备像素比；
  - 开启「减少动态效果」或浏览器不支持 WebGL 时，降级为静态渐变。
- 主题切换用 View Transitions 做圆形扩散，浏览器不支持时直接切换。

### 3.5 页面与路由

| 路由 | 页面 | 说明 |
|---|---|---|
| `/login` | 登录 | 密码登录、验证码登录两个标签 |
| `/register` | 注册 | |
| `/chat` | 会话列表 + 空状态 | 登录后的默认页 |
| `/chat/:conversationId` | 会话 | 群资料和成员面板是界面状态，不单独占路由 |
| `/chat/with/:userId` | 草稿单聊 | 和这个好友还没有会话时使用，第一条消息确认后替换为 `/chat/:conversationId` |
| `/contacts`、`/contacts/requests`、`/contacts/blocked` | 好友、申请、黑名单 | |
| `/groups` | 我的群 | 建群用对话框，不单独占路由 |
| `/settings/profile`、`/settings/devices`、`/settings/appearance` | 资料、设备、外观 | |
| `/admin` | 封禁与解封 | 只有 `ROLE_ADMIN` 可见 |

未登录时访问受保护的路由，跳转到 `/login?redirect=<原路径>`。

### 3.6 关键交互

#### 3.6.1 登录与注册

- **布局**：左右分屏。
  - 左侧是深色品牌区：火星红 WebGL 背景（候选 React Bits 的 Soft Aurora、Grainient、Silk，原型阶段对比）、标题文字动画（Split Text 或 Blur Text），再用 Rotating Text 轮播卖点，比如「断线不丢消息」「多端同步」；
  - 右侧是表单。窄屏时品牌区收成顶部的一条横幅。
- **验证码**：
  - 用 6 位分格输入框，支持粘贴；
  - 「重新发送」按钮的倒计时和后端的发送间隔一致；
  - 后端返回限流错误时，按 metadata 中的剩余秒数显示倒计时。
- **密码**：规则实时提示（8～64 位可打印字符，必须同时包含字母和数字），和后端的 protovalidate 规则一致；登录失败次数过多被锁定时，显示剩余时间。
- **登录成功**：卡片收起，过渡到主界面（`gentle`）。

#### 3.6.2 会话列表

- 每一项显示：头像、名称、最后一条消息的摘要、时间、未读数（沙金色徽章）。超过 99 条显示 `99+`。
- 收到新消息时，对应会话用 `layout` 动画平滑移到顶部，未读数用 `snappy` 弹一下。
- 浏览器标签页标题显示未读总数，比如 `(3) MarsChat`。

#### 3.6.3 消息时间线

- **分组**：
  - 同一发送者 5 分钟内的连续消息合并为一组，只显示一次头像和昵称；
  - 跨天时插入日期分隔线；
  - 打开会话时，在第一条未读消息前插入「以下为新消息」分隔线。
- **滚动**：
  - 向上滚动时按游标加载历史，加载过程中保持滚动位置不跳；
  - 不在底部时收到新消息，「回到底部」按钮上显示新消息数。
- **我方消息的状态**：显示在气泡右下角，用白色图标。

| 状态 | 条件 | 显示 |
|---|---|---|
| 发送中 | 已放入发送队列，还没收到 `SendAck` | 时钟图标，气泡透明度 70% |
| 已发送 | 收到 `SendAck` | 单勾 |
| 已读（只有单聊） | 会话的 `peer_read_seq` 大于等于这条消息的 seq | 双勾 |
| 失败 | 不可重试的错误，或者自动重试用完 | 气泡外侧显示错误图标、原因和「重试」按钮，重试时使用同一个 `client_msg_id` |

- **气泡入场**：我方气泡从输入框方向轻微上移、淡入；对方气泡淡入（`smooth`）。

#### 3.6.4 输入框

- Enter 发送，Shift+Enter 换行，最多自动增高 6 行。
- 用输入法输入中文时（`compositionstart` 到 `compositionend` 之间，或 `KeyboardEvent.isComposing` 为真），按 Enter 是确认选词，不能发送。
- 正文上限 4000 个字符（与 `TextContent` 的校验一致），接近上限时显示剩余字数。
- 当前会话不能发言时（被禁言、全员禁言、群已解散、不是好友），输入框替换为说明条。原因来自最近一次发送失败的错误，或者群资料和成员资料。

#### 3.6.5 连接状态胶囊

聊天区顶部有一个类似灵动岛的小胶囊，在不同状态之间变形（用 `layout` 动画）。它把后端的断线补拉直接展示出来，是全站最能体现后端可靠性的设计。

| 连接状态 | 胶囊 |
|---|---|
| 连接中、认证中 | 「连接中…」，带脉冲点 |
| 补拉中 | 「正在同步 N 条消息」，N 是本轮 `GetDifference` 累计拿到的条数 |
| 已同步 | 显示「已同步」约 1.5 秒后收起，只剩一个小圆点 |
| 重连等待 | 「连接已断开，3 秒后重连」，可以点击立即重连 |
| 浏览器离线 | 「网络已断开」，收到 `online` 事件后立即重连 |
| 服务停机（1001） | 「服务器维护，正在切换…」，随后按重连流程走 |

#### 3.6.6 错误与异常

| 场景 | 表现 |
|---|---|
| 被踢下线（`Kicked` 后以 4002 关闭） | 全屏遮罩，按 `RevokeReason` 说明原因（在其他设备上退出、被踢、被封禁、同一设备重新登录、令牌疑似泄露），确认后清除本地登录状态并回到登录页 |
| 刷新令牌失效 | 同上，原因写「登录已过期」 |
| 发送太频繁（`RATE_LIMITED`） | 顶部提示，按 `retry_after_seconds` 倒计时，到时自动重发（3.9.4） |
| 发言被拒（`NOT_FRIENDS`、`BLOCKED`、`NOT_GROUP_MEMBER`、`MEMBER_MUTED`、`GROUP_MUTED`、`GROUP_DISMISSED`） | 这条消息标为失败并显示原因；输入框替换为说明条 |
| 网关限流（HTTP 429，响应体 `error_msg`） | 提示「操作太频繁，请稍后再试」 |
| 依赖不可用（503） | 列表页显示可以重试的错误卡片；资料缺失时（后端降级返回空资料），头像显示占位，名称显示「用户 <ID>」 |

所有错误原因到提示文案的映射放在 `web/src/api/errors.ts` 中统一维护，不在各个页面里各写一份。

#### 3.6.7 联系人、群组与通知

- **我的 ID**：显示在个人卡片和「添加好友」对话框里，用等宽字体，点击复制，复制后图标有一个勾选动效。
- **申请**：同意或拒绝后，卡片带动画移出（`AnimatePresence`）。
- **成为好友、建群成功**：播放一次庆祝动效（React Bits 的 Click Spark 或自己用 Motion 做粒子）。这是全站唯一「热闹」的时刻。
- **建群向导**：用 Stepper 分三步：名称、头像、加群方式 → 邀请好友 → 完成。
- **群成员**：
  - 列表带角色徽章（群主、管理员），被禁言的成员显示剩余时间；
  - 转让、解散、移除都要二次确认，解散需要输入群名确认。
- **通知**：

| `Notification` 类型 | 提示 | 刷新 |
|---|---|---|
| `friend_request_created` | 「X 请求添加你为好友」，带「查看」按钮 | 收到的申请、导航栏徽章 |
| `friend_added`、`friend_deleted` | 成为好友时有提示，删除好友时不提示 | 好友列表 |
| `group_created`、`group_members_joined`、`group_member_left` | 自己被拉进群或被移出群时提示 | 我的群、成员列表、会话列表 |
| `group_member_updated`、`group_updated` | 自己被设为管理员、被禁言时提示 | 群资料、成员列表 |
| `group_dismissed` | 「群 X 已解散」 | 我的群、会话（保留会话，只能看历史） |
| `group_join_request_created` | 群主和管理员看到「X 申请加入群 Y」 | 入群申请 |

收到的 `event_id` 去重后再处理。重连成功后，刷新好友、申请和群列表，补上断线期间错过的变化。

#### 3.6.8 设置

- **资料编辑**：昵称、性别、生日、签名；头像要等文件阶段完成才能上传。头像缺省时显示昵称首字母，背景是由用户 ID 计算出的暖色渐变，同一个用户在任何设备上颜色都一样。
- **设备管理**：每台设备一张卡片，显示平台图标、设备名、最近活跃时间、IP，当前设备有标记；踢掉设备时卡片带动画移出。
- **外观**：亮色、深色、跟随系统。

#### 3.6.9 文件（后端步骤 6 完成后）

按 [media-service.md](media-service.md) 的接口：

- **上传**：`POST /v1/files?purpose=&name=`，请求体是文件本身。`fetch` 拿不到上传进度，所以上传用 `XMLHttpRequest`，以便显示进度条。上传前在前端检查大小上限，提示更早；最终仍以服务端的校验为准。
- **图片消息**：按 `width`、`height` 预留占位，加载缩略图时不让布局跳动；点击用大图查看器打开原图。
- **文件消息**：卡片显示文件名、大小、类型图标，点击后换取下载地址再下载。
- **下载地址**：批量换取（每次最多 100 项），按文件 ID 和变体缓存，过期前复用（有效期 10 分钟）。
- **头像**：上传时 `purpose=PURPOSE_AVATAR`，再把文件 ID 写入资料。

### 3.7 前端架构与状态归属

| 数据 | 归属 | 原因 |
|---|---|---|
| 会话列表、消息时间线、已读位置、发送队列 | 同步引擎（`realtime/`）加 zustand store | 由 HTTP 加载和实时推送共同维护，需要按 seq 合并、去重 |
| 好友、申请、黑名单、群、群成员、资料、设备 | React Query | 普通 HTTP 列表，收到 `Notification` 后让对应查询失效、重新获取 |
| 连接状态 | zustand store（连接状态机写入） | 胶囊、输入框、页面横幅都要读取 |
| 令牌、设备 ID、主题 | `localStorage` 加内存缓存 | 多个标签页之间共享 |

HTTP 客户端（`api/http.ts`）：

- 请求使用相对路径，带 `Authorization: Bearer <访问令牌>`，请求体和响应体都是 protobuf JSON，用生成代码的 `toJson`、`fromJson` 转换。
- 把 Kratos 错误 `{code, reason, message, metadata}` 和网关 429 的 `{error_msg}` 统一解析为 `ApiError`。
- 收到 401 时刷新令牌，然后重试一次。

### 3.8 认证与令牌

- **设备 ID**：首次打开时用 `crypto.randomUUID()` 生成，去掉连字符后得到 32 个字符，满足 `^[A-Za-z0-9_-]+$` 和 8～64 的长度要求，保存在 `localStorage`。同一个浏览器的所有标签页共用这个设备 ID，因此共用同一个登录会话。
- **设备信息**：`platform` 固定为 `PLATFORM_WEB`；`device_name` 由 User-Agent 推断，比如「Chrome · Windows」，最长 64 个字符。
- **存储**：`TokenPair` 和用户 ID 存在 `localStorage`，同一浏览器的所有标签页共享。
- **刷新时机**：
  - 访问令牌过期前 60 秒主动刷新；
  - HTTP 返回 401 时，刷新后重试一次。
- **多标签页同时刷新**：
  - 用 Web Locks API（`navigator.locks.request`）串行化刷新；
  - 拿到锁后，先看 `localStorage` 中的令牌是否已经被别的标签页更新过，更新过就直接使用；
  - 仍然收到 `REFRESH_TOKEN_ROTATED` 时，改用 `localStorage` 中最新的令牌；
  - 用 `storage` 事件同步其他标签页的登录和退出。
- **刷新失败**（令牌无效、会话已吊销、用户被封禁）：清除本地登录状态，回到登录页并说明原因。
- **长连接**：连接不会随访问令牌过期而断开（data-flow 3.2）。重连时使用最新的访问令牌，快过期时先刷新；收到 4001 时，刷新后再重连。
- **风险**：刷新令牌放在 `localStorage`，一旦出现 XSS 就可能被窃取。改用 HttpOnly Cookie 需要 account 修改接口并增加 CSRF 防护，本设计不做（5.8、第 9 节）。缓解措施：消息只按纯文本渲染；不引入第三方脚本；部署时加 CSP。

### 3.9 实时连接与同步引擎

完全按照 data-flow 第 3、5.2、7 节的客户端约定实现。

#### 3.9.1 连接

1. 建立连接 `new WebSocket("/ws", ["marschat.v1.proto"])`，设置 `binaryType = "arraybuffer"`，帧用生成代码编解码。JSON 子协议只用于调试。
2. 打开后立即发送 `Auth{access_token}`，后端要求 10 秒内完成。
3. 收到 `AuthOk` 后，按其中的 `heartbeat_interval_seconds`（30 秒）定时发送 `Ping`。连续 2 个心跳周期没有收到任何帧，就判定服务端失联，主动关闭并重连。
4. 页面切到后台时不断开连接；收到浏览器的 `online` 事件时，立即尝试重连。

每个标签页各自建立一条连接，理由见 5.7。

#### 3.9.2 关闭码与重连

| 关闭码 | 处理 |
|---|---|
| 4001 | 刷新令牌后重连；刷新失败则回到登录页 |
| 4002 | 不重连，按 `Kicked.reason` 显示被踢下线的遮罩（3.6.6） |
| 4003 | 重连并补拉 |
| 4008、1009 | 记录错误日志后重连（正常客户端不应出现，出现说明前端有 bug） |
| 1001、1006、其他 | 随机等待 0～2 秒后重连 |

连续失败时按指数退避重连：1、2、4、8 秒……最长 30 秒，每次再加 0～2 秒随机抖动。认证成功后，失败计数清零。

#### 3.9.3 同步

每个会话维护以下状态：

- `maxSeq`：服务端已知的最新 seq；
- `contiguousMaxSeq`：本地连续拿到的最大 seq；
- `readSeq`、`peerReadSeq`；
- 已加载的消息：按 seq 存放；
- 是否还有更早的历史。

同步步骤：

1. 认证成功后，分页拉取全部会话（`GET /v1/conversations`，`limit=100`），更新会话摘要。
2. 只为**已经加载过时间线**的会话补拉：服务端 `max_seq` 大于本地 `contiguousMaxSeq` 时，调用 `GetDifference(from_seq=contiguousMaxSeq, limit=200)`。
   - `COMPLETE`：补拉完成；
   - `SLICE`：用 `next_from_seq` 继续；
   - `TOO_LONG`：丢弃这个会话的时间线，用 `ListMessages` 重新加载最新一页，并在顶部标注「已跳到最新消息」。

   没有打开过的会话不拉消息，打开时用 `ListMessages` 加载最新一页。
3. 收到 `Message` 推送：
   - seq 等于 `contiguousMaxSeq + 1`：追加到时间线；
   - seq 小于等于 `contiguousMaxSeq`：按 `(conversation_id, seq)` 去重，丢弃；
   - seq 更大：说明中间有缺口，按第 2 步补拉；
   - 时间线没有加载过：只更新会话摘要和未读数。
4. 收到不认识的会话的消息：调用 `GET /v1/conversations/{id}` 取会话信息，然后插入列表。
5. 收到 `Read` 推送：本人的推送更新 `readSeq`；对方的推送更新 `peerReadSeq`。不大于本地值时忽略。
6. 上报已读：会话可见、窗口有焦点、并且滚动到底部时，防抖 500 毫秒后调用 `POST .../read`，`seq` 取可见的最大 seq。

#### 3.9.4 发送队列

- 每条消息生成一个 `client_msg_id`（`crypto.randomUUID()`，36 个字符，满足格式要求）。`request_id` 在每条连接内递增。
- 消息先放入发送队列并立即显示（「发送中」）。连接可用时发送 `SendMessage`；同一条连接上的确认按发送顺序返回。
- 收到 `SendAck`：写入 seq 和 `created_at`，从队列中移到时间线（按 seq 去重）。如果是草稿单聊，把路由替换为 `/chat/:conversationId`，并与会话列表合并。
- 错误分两类处理：

| 错误 | 处理 |
|---|---|
| 连接断开、超过 10 秒没有确认、`UPSTREAM_UNAVAILABLE`、`CIRCUITBREAKER`、`DEPENDENCY_UNAVAILABLE` | 结果未知或可以重试。重连后用同一个 `client_msg_id` 自动重发，最多 3 次，之后标为失败，可以手动重试 |
| `RATE_LIMITED` | 等待 `retry_after_seconds` 秒后自动重发 |
| `VALIDATOR`、发言被拒的各种原因 | 不自动重试，标为失败并显示原因 |

- 发送队列只存在内存里。刷新页面会丢掉还没确认的消息，离开页面前如果有未确认的消息，弹出浏览器的离开确认（`beforeunload`）。

### 3.10 开发与部署

- **开发服务器**：`pnpm dev` 启动 Vite。代理目标由环境变量 `MARSCHAT_API_TARGET` 选择：
  - `gateway`（默认）：`/v1`、`/ws` 都转发到 APISIX `http://127.0.0.1:19080`（容器模式）；
  - `host`：按路径前缀转发到宿主机上运行的各个服务。

| 路径 | 宿主机模式的目标 |
|---|---|
| `/v1/auth`、`/v1/users`、`/v1/sessions`、`/v1/admin` | `127.0.0.1:18010` |
| `/v1/friend-requests`、`/v1/friends`、`/v1/blocks`、`/v1/groups`、`/v1/group-join-requests` | `127.0.0.1:18020` |
| `/v1/conversations` | `127.0.0.1:18030` |
| `/v1/files` | `127.0.0.1:18050`（media 完成后） |
| `/ws` | `ws://127.0.0.1:18000` |

- **生成代码**：
  - 新增 `buf.gen.web.yaml`：输入 `api/`，插件 `protoc-gen-es`（`target=ts`），输出到 `web/src/gen/`；
  - `buf/validate`、`google/api` 等依赖要一起生成（`include_imports`），具体写法在实施时验证；
  - 生成代码提交到仓库，`make gen` 同时生成 Go 和 TS 代码，CI 检查两者与 proto 一致。
- **部署**：构建产物是静态文件。原则是与网关同源，不开 CORS。具体方式（例如静态文件容器经 APISIX 的 `/` 路由提供）在计划第 8 阶段单独设计；涉及 Compose 和网关配置的改动，届时先确认再实施。

## 4. 数据结构与接口

- **后端接口**：不新增也不修改。前端用到的接口都已存在，见 [api/openapi.yaml](../../api/openapi.yaml)、[frame.proto](../../api/access/v1/frame.proto)；文件接口见 media 设计第 4.2 节。
- **`localStorage` 键**：

| 键 | 内容 |
|---|---|
| `marschat.device` | `{ id }` |
| `marschat.auth` | `{ userId, accessToken, accessExpiresAt, refreshToken, refreshExpiresAt }` |
| `marschat.theme` | `light`、`dark` 或 `system` |

- **前端内部模型**（`realtime/`）：

```ts
// 一个会话的本地时间线；ID 和 seq 是 protobuf-es 的 bigint，作为 Map 键时转成字符串
interface Timeline {
  messages: Map<string, Message>;   // 键为 seq
  contiguousMaxSeq: bigint;         // 本地连续拿到的最大 seq，补拉的起点
  oldestLoadedSeq: bigint;          // 已加载的最早 seq，向上翻页的游标
  hasMoreBefore: boolean;
}

interface OutgoingItem {
  clientMsgId: string;
  peerType: ConversationType;
  peerId: bigint;
  content: MessageContent;
  status: "pending" | "failed";
  error?: ApiError;
  attempts: number;
  enqueuedAt: number;
}
```

## 5. 候选方案与权衡

### 5.1 代码位置

| 方案 | 优点 | 缺点 | 结论 |
|---|---|---|---|
| 本仓库 `web/` | 改 proto 和帧协议时，生成代码、前后端代码能在同一个提交里一起改（AGENTS.md 要求帧变化时同步 proto 和 data-flow）；求职展示只需要一个仓库 | CI 要同时装 Go 和 Node；仓库变大 | **推荐** |
| 独立私有仓库 | 依赖和 CI 完全隔离 | proto 要跨仓库同步，生成代码要发布或复制；同一个功能要提交到两个仓库 | 不推荐 |

后端重构计划中「技术栈完全不同的部分可以拆出独立仓库」的条件，前端是满足的。但它需要的 proto 契约在本仓库里，拆出去的收益抵不过同步成本。

### 5.2 构建框架

Vite SPA 与 Next.js 相比：页面全部在登录后访问，不需要 SEO 和服务端渲染；后端是独立的服务，不需要 BFF。Vite 更简单，所以选 Vite。

### 5.3 组件库

shadcn/ui 与 Ant Design、MUI 相比：后两者自带很强的中后台风格，很难做出品牌感；shadcn/ui 的源码在仓库里，样式可以完全按设计规范修改。而且 React Bits 也通过 shadcn CLI 安装，工具链统一。

### 5.4 消息本地缓存

| 方案 | 优点 | 缺点 |
|---|---|---|
| 只放内存（推荐） | 简单；不用处理多标签页一致性、存储结构迁移和清理 | 刷新页面后重新加载最新一页，冷启动多几个请求 |
| IndexedDB | 冷启动快，离线也能看历史 | 多标签页并发写入、版本迁移、容量清理、退出登录时清除，都要额外处理 |

先只放内存，以后有明确需求时再加 IndexedDB。同步引擎按「本地连续的最大 seq」工作，以后加持久化不需要改协议。

### 5.5 状态管理

zustand 加 React Query，与 Redux Toolkit 相比样板代码更少。实时数据（会话、消息）和普通 HTTP 列表分开管理，归属清楚（3.7）。

### 5.6 WebSocket 编码

默认用 protobuf 二进制，与后端的默认子协议一致，类型和编解码都由 proto 生成。JSON 子协议只在调试时使用。

### 5.7 每个标签页一条连接，还是共享一条

用 SharedWorker 共享连接可以减少连接数，但 Android 版 Chrome 不支持 SharedWorker，调试也更麻烦。后端的多端同步本来就覆盖了多个标签页的情况，网关允许每个 IP 50 条并发连接。所以先采用每个标签页一条连接。

### 5.8 令牌存储

| 方案 | 优点 | 缺点 |
|---|---|---|
| `localStorage`（推荐） | 后端不用改；多个标签页天然共享 | 出现 XSS 时可能被窃取 |
| HttpOnly Cookie | 脚本读不到刷新令牌 | account 要改接口（下发和读取 Cookie）、增加 CSRF 防护，还要处理网关与开发代理下的 Cookie 域 |

先用 `localStorage`，依靠 3.8 的缓解措施。如果项目要公开演示，再评估改用 Cookie。

### 5.9 路由

React Router 与 TanStack Router 相比：后者路由类型安全更强，但概念更多。本项目只有十来个路由，React Router 足够。

## 6. 新增依赖与环境变化

- **新增文件**：
  - `web/` 下的 `package.json`、`pnpm-lock.yaml`、`pnpm-workspace.yaml`。pnpm 11 会把未批准的依赖安装脚本当作错误，按 ADR-0004 的做法在 `allowBuilds` 中逐个批准；
  - `buf.gen.web.yaml`。
- **修改**：
  - Makefile：新增 `web-dev`、`web-check` 等目标，`make gen` 同时生成 TS；
  - `.gitignore`：忽略 `web/node_modules/`、`web/dist/`；
  - CI：新增前端任务（`pnpm install --frozen-lockfile`、lint、类型检查、单元测试、构建）和 TS 生成代码一致性检查。
- **Go 侧**：没有变化，`go.mod` 不变。
- **依赖审批**：本设计确认后，第 3.1 节列出的依赖视为已同意，安装时锁定当时的稳定版本；计划外的新依赖（包括字体包、Playwright）仍需先询问。

## 7. 风险与验证

### 7.1 风险

| 风险 | 应对 |
|---|---|
| WebGL 背景在低端设备上卡顿，或浏览器不支持 | 只用在登录页并懒加载；限制设备像素比；标签页不可见时暂停；开启「减少动态效果」或不支持 WebGL 时降级为静态渐变 |
| React Bits 组件与 Tailwind 4 不兼容，或依赖 three.js 体积过大 | 原型阶段逐个验证；不合适就换候选组件，或用 CSS 渐变自己实现 |
| TypeScript 7 与 typescript-eslint、Vite 插件不兼容 | 搭建脚手架时验证；不兼容就回退到工具链支持的 TypeScript 版本，并在计划中记录 |
| Windows 下 buf 调用 `node_modules` 中的 `protoc-gen-es`，以及依赖 proto 的生成方式 | 工程骨架阶段先验证，把可行的写法记录到本设计新增的「实施中的调整」一节 |
| 刷新令牌放在 `localStorage`，存在 XSS 风险 | 见 3.8 的缓解措施和 5.8 |
| 本机经网关的请求共用一个 IP，开发时容易触发认证接口的入口限流 | 开发时默认用宿主机模式；需要验证网关时再切到网关模式 |
| 动效堆得太多，显得廉价，或干扰聊天 | 遵守 3.4.1 的原则；原型评审时逐项检查动效清单，高频区域只保留必要的动效 |

### 7.2 验证

- **单元测试**（vitest，不依赖后端）：
  - 同步引擎：顺序追加、重复丢弃、缺口触发补拉、`SLICE` 循环、`TOO_LONG` 重新加载、不认识的会话、已读位置只增不减；
  - 发送队列：确认、错误分类、同一个 `client_msg_id` 重发、限流等待、草稿会话替换为真实会话；
  - 令牌管理：提前刷新、401 后重试一次、`REFRESH_TOKEN_ROTATED`、多标签页加锁；
  - 错误解析：Kratos 错误和网关 429 两种格式；
  - 关闭码处理和退避计算。
- **组件测试**：输入法选词时按 Enter 不发送；消息四种状态的显示；被踢遮罩。
- **检查顺序**：`pnpm lint` → `pnpm typecheck` → `pnpm test` → `pnpm build`，并记录产物体积。
- **联调**：
  - 宿主机模式和容器模式各走一遍主流程；
  - 两个浏览器（两个用户）互发消息；
  - 同一用户开多个标签页，检查多端同步和已读回执。
- **故障观察**（手动，口径与 `make cluster-test` 一致）：容器模式下 `docker kill` 一个 access、滚动重启 access。观察连接胶囊的状态变化，检查重连后消息不丢、不重复，seq 连续。
- **视觉评审**：亮色、深色、窄屏三种截图；对比度用脚本核对设计变量。

## 8. 实施顺序

见[前端计划](../plans/active/frontend-rewrite.md)：设计确认 → 工程骨架 → 视觉原型（假数据）→ 认证 → 实时连接与消息 → 联系人与群组 → 设置与管理 → 文件 → 打磨与部署。

先做视觉原型，是为了在接入后端之前定下配色、字体和动效基调，避免接完数据之后再大改样式。

## 9. 确认结果（2026-11-20）

开发者确认以下 6 个问题均按推荐方案执行，选型记录见 [ADR-0008](../architecture/adr/0008-react-frontend.md)。

1. **代码位置**：推荐放在本仓库 `web/`（5.1）。
2. **主题**：推荐默认亮色，登录注册页固定深色，同时提供可以切换的深色主题（3.4.3）。
3. **Aceternity UI**：推荐只参考设计，不把源码提交进仓库（3.3）。
4. **令牌存储**：推荐先用 `localStorage`，以后公开演示时再评估 HttpOnly Cookie（5.8）。
5. **消息缓存**：推荐只放内存，暂不做 IndexedDB（5.4）。
6. **依赖清单**：确认第 3.1 节的依赖，确认后安装这些依赖视为已同意（第 6 节）。

## 10. 实施中的调整

1. **TypeScript 锁定 6.0.3**（2026-11-20，核对依赖时）：第 3.1 节写的是 7.0.2，但 typescript-eslint 8.73.0 的 peer 依赖要求 TypeScript `>=4.8.4 <6.1.0`。按第 7 节的应对方案，回退到 6.0 系列的最新版 6.0.3，ESLint 不变。

   Vite 官方 `react-ts` 模板（create-vite 9.3.4）已改用 TypeScript 7.0.2 加 oxlint，不再用 ESLint。改用 oxlint 属于更换依赖，需要开发者另外确认。typescript-eslint 支持 TypeScript 7 后，再升级。

以下为第 1 阶段（工程骨架，2026-11-21）中的调整：

2. **尘灰改为 `#6E635C`**：初稿的 `#7A6F68` 在月岩白上是 4.57，但放在 `--muted`（`#F1ECE6`）背景上只有 4.16。对比度测试覆盖了这个组合后发现问题，改为 `#6E635C`（4.96）。
3. **变量名沿用 shadcn/ui 的约定**：`background`、`foreground`、`card`、`primary`、`muted`、`accent`、`destructive`、`ring` 等，shadcn 组件可以直接使用。shadcn 的 `accent` 表示悬停和选中背景，所以初稿中叫 `--accent` 的沙金色改名为 `--gold`。另外新增两个变量：
   - `--brand-text`：红色文字。亮色主题用深火星红，深色主题用亮火星红，因为深火星红在深色背景上对比度不够；
   - `--gold-foreground`：沙金背景上的文字颜色。
   
   圆角和阴影另外命名（`--card-radius`、`--card-shadow` 等），再在 `@theme inline` 中映射为 Tailwind 的 `rounded-card`、`shadow-card`，避免与 Tailwind 主题变量同名形成自引用。
4. **shadcn 初始化**：
   - 用 `shadcn init -b base -p nova`。组件库选 Base UI，这是 CLI 的默认值；初稿写的 `base-nova` 是 `components.json` 中的 style 名，不是 CLI 接受的预设名。
   - shadcn 4 用 shadcn 维护的 `cn` 包（MIT，仓库 `shadcn-ui/cn`）替代 `clsx` 加 `tailwind-merge`；`shadcn` 包作为依赖提供 `shadcn/tailwind.css`。
   - 预设顺带安装的字体包 `@fontsource-variable/geist` 已移除：字体在原型阶段对比后再请开发者确认。
5. **tsconfig 不开启 `erasableSyntaxOnly`**：`protoc-gen-es` 默认把枚举生成为 TypeScript `enum`，与这个选项冲突。插件的替代选项 `erasable_syntax` 还是实验性的，所以保留生成器的默认输出，tsconfig 去掉这个选项。
6. **暂不建 `pnpm-workspace.yaml`**：这次安装没有依赖需要批准安装脚本，等出现时再按 ADR-0004 的做法添加。
7. **Vitest 读取 CSS**：Vitest 默认把 CSS 替换为空字符串，`?raw` 导入也拿到空串。所以在 `test.css.include` 中只对 `tokens.css` 开启处理。
8. **依赖按需安装**：第 3.1 节中的 react-router、@tanstack/react-query、zustand、sonner、@tanstack/react-virtual、@testing-library/react、jsdom，在第一次用到的阶段再安装，不在骨架阶段预装。它们仍属于已同意的依赖。
