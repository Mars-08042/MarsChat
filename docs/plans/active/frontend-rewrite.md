# 计划：React 前端重写

- 状态：进行中 | 开始：2026-11-20 | 分支：`dev` | 关联设计：[frontend.md](../../design/frontend.md)（已采纳）、[ADR-0008](../../architecture/adr/0008-react-frontend.md)

## 目标

用 React + pnpm 重写前端，覆盖新后端已有的全部用户功能。视觉以火星红为唯一强调色，暖色中性色为底，提供亮色和深色两套主题。按 [data-flow.md](../../architecture/data-flow.md) 的客户端约定实现连接与同步，把后端的可靠性（发送确认、幂等重发、断线补拉、多端同步、已读回执）做成用户看得见的交互。

## 范围与约定

- 前端代码放在本仓库 `web/`（2026-11-20 确认，ADR-0008）。包管理用 pnpm（[ADR-0004](../../architecture/adr/0004-frontend-pnpm.md)）。
- 不新增或修改后端接口。发现需要后端配合时，先记录到[后端重构计划](backend-refactor.md)或 [tech-debt.md](../tech-debt.md)，再与开发者确认。
- 和后端计划的依赖关系：
  - 第 7 阶段（文件）依赖后端步骤 6（media）完成；
  - 音视频通话依赖后端步骤 7，不在本计划内，届时追加。
- 依赖：设计确认后，设计第 3.1 节列出的依赖视为已同意，安装时锁定当时的稳定版本；计划外的新依赖仍先询问。
- Compose、网关配置、CI 的改动属于对应阶段的任务；推送到远端仍需开发者同意。
- 每个阶段完成时，按 AGENTS.md 的文档同步表更新文档，并运行该阶段列出的验证。

## 步骤

### 0. 设计确认

- [x] 调研组件来源：React Bits、Aceternity UI、hover.dev、uiverse.io、minimal.gallery、Lucide、Motion（2026-11-20）。结论与许可证核对见设计第 3.3 节。
- [x] 编写设计文档（提议中）和本计划（2026-11-20）。
- [x] 开发者确认设计第 9 节的 6 个问题，均按推荐方案；设计状态改为已采纳（2026-11-20）。
- [x] 新增 [ADR-0008](../../architecture/adr/0008-react-frontend.md)，记录前端技术栈与代码位置，同时更新 ADR 索引（2026-11-20）。

### 1. 工程骨架

- [x] `web/`：Vite 8 + React 19 + TypeScript 6.0.3、Tailwind CSS 4、shadcn/ui（Base UI，nova 预设）、ESLint、Vitest、`@/` 路径别名。这次安装没有依赖需要批准安装脚本，暂不建 `pnpm-workspace.yaml`（设计第 10 节）。
- [x] 设计变量写在 `web/src/styles/tokens.css`（颜色、圆角、阴影，亮色和深色），`index.css` 映射为 Tailwind 主题；动效参数在 `src/lib/motion.ts`，`MotionConfig` 设置默认参数和 `reducedMotion="user"`。`tokens.test.ts` 按 WCAG 公式核对 21 组颜色组合（两套主题共 42 项，加 1 项已知结论）；变异验证中，把尘灰改回初稿的 `#7A6F68` 时测试失败，于是调整为 `#6E635C`。
- [x] 新增 `buf.gen.web.yaml`，生成 TS 代码到 `web/src/gen/`（18 个文件）。Windows 下用 `node` 直接运行插件入口；`include_imports` 一并生成 `buf/validate`、`google/api`；`make gen` 同时生成 Go 和 TS。
- [x] Vite 代理支持 `gateway`（默认）和 `host` 两种模式（`MARSCHAT_API_TARGET`）。
- [x] Makefile 新增 `web-gen`、`web-install`、`web-dev`、`web-check`；`.gitignore` 忽略 `node_modules/`、`web/dist/`；CI 新增 pnpm 与 Node 24、安装前端依赖（放在生成代码检查之前）和 `pnpm check`。
- [x] 新建 `web/THIRD_PARTY_NOTICES.md`。
- [x] 文档：`deployment.md`（运行依赖、前端开发、开发命令、CI）、`system.md`（仓库结构、代码生成）、`AGENTS.md`（导航、前端约束、验证命令）、设计第 3.4.2 节和第 10 节。
- 验证（2026-11-21，`ui` 分支 worktree）：
  - `pnpm lint`、`pnpm typecheck`、`pnpm test`（43 项）、`pnpm build` 通过，产物 JS 221.03 KB（gzip 69.28 KB）、CSS 11.64 KB（gzip 3.12 KB）；
  - `make web-gen` 连续执行两次，18 个文件完全一致；`make gen` 后 Go 侧没有差异；`buf lint`、`go vet ./...`、`go build ./...` 通过；
  - `pnpm dev` 首页正常返回；浏览器中亮色背景、正文颜色，以及 `.dark` 下的深色背景都与设计变量一致；后端未运行时，代理请求返回 502。
  - 未验证：代理连到真实后端（第 3 阶段联调时验证）；CI（未推送）。

### 2. 视觉原型（假数据）

- [ ] 登录注册页：对比 2～3 个 WebGL 背景候选（Soft Aurora、Grainient、Silk）和标题动效；核对每个组件的依赖，以及与 Tailwind 4 的兼容性。
- [ ] 主界面三栏：会话列表、消息时间线（分组、日期分隔线、四种消息状态）、输入框、连接状态胶囊的全部状态（可以手动切换演示）。
- [ ] 对比 2 组英文标题字体和等宽字体，选定后追加字体依赖（需询问）。
- [ ] 亮色、深色、窄屏三种截图交开发者评审；把定稿的字体、背景和动效参数写回设计第 3.4 节。
- 验证：截图评审；对比度用脚本核对设计变量；记录构建产物体积；开启「减少动态效果」和禁用 WebGL 时检查降级效果。

### 3. 认证

- [ ] HTTP 客户端与错误解析（Kratos 错误和网关 429 两种格式），错误文案统一放在 `api/errors.ts`。
- [ ] 令牌管理：设备 ID、提前刷新、401 后重试一次、Web Locks 串行刷新、`REFRESH_TOKEN_ROTATED`、用 `storage` 事件同步其他标签页。
- [ ] 登录（密码、验证码）、注册、退出、路由守卫、登录态失效页。
- 验证：单元测试（令牌管理、错误解析）；宿主机模式联调，开发环境的验证码固定为 `000000`；多标签页同时刷新时会话不被吊销。

### 4. 实时连接与消息

- [ ] 连接状态机：认证、心跳、失联检测、关闭码处理、指数退避加随机抖动、`online` 事件。
- [ ] 同步引擎：会话列表、时间线、补拉的三种结果、去重、未知会话、已读位置与上报。
- [ ] 发送队列：`client_msg_id`、按错误分类重试、限流等待、草稿单聊、`beforeunload` 提示。
- [ ] 界面：会话列表（重排动画、未读数、标签页标题）、消息时间线（虚拟列表、加载历史、未读分隔线、回到底部）、输入框（输入法选词时不误发、字数上限）、连接状态胶囊、被踢遮罩、发送失败与不能发言的提示。
- 验证：
  - 单元测试（同步引擎、发送队列、关闭码与退避）；组件测试（输入法、消息状态）；
  - 联调：两个用户互发；同一用户多个标签页；已读回执；被踢下线；
  - 容器模式下 `docker kill` 一个 access、滚动重启 access：观察连接胶囊，检查消息不丢、不重复，seq 连续。

### 5. 联系人与群组

- [ ] 好友列表、收到和发出的申请、黑名单、按 ID 添加好友、我的 ID 与复制。
- [ ] 建群向导、群资料与修改、全员禁言、加入或申请、审批、邀请好友、成员管理（角色、移除、单人禁言）、转让、退群、解散。
- [ ] 处理 `Notification` 帧：按 `event_id` 去重、弹出提示、让对应查询失效；重连后刷新各个列表。
- [ ] 成为好友、建群成功时的庆祝动效。
- 验证：单元测试（通知到查询失效的映射）；联调主要流程，包括第三个用户在线时收到群通知。

### 6. 设置与管理

- [ ] 资料编辑、他人资料卡片、设备管理（踢设备）、外观（亮色、深色、跟随系统，View Transitions 切换）。
- [ ] 管理页：按用户 ID 封禁、解封；被封禁的用户在线时立即被踢下线。
- 验证：联调；踢掉设备后，被踢的标签页显示遮罩并回到登录页。

### 7. 文件（依赖后端步骤 6）

- [ ] 用 `XMLHttpRequest` 上传并显示进度；上传前检查大小上限；处理错误原因。
- [ ] 头像上传与显示；图片消息（按宽高预留占位、缩略图、大图查看）；文件消息（卡片、下载）。
- [ ] 下载地址批量换取，过期前复用。
- 验证：单元测试（下载地址缓存）；联调本地存储模式；上传 100 MB 文件和超限文件。

### 8. 打磨与部署

- [ ] 逐项复核动效清单、「减少动态效果」降级、键盘可达性、对比度、产物体积。
- [ ] 设计部署方式（与网关同源，例如静态文件容器经 APISIX 的 `/` 路由提供）。修改 Compose 和网关配置前先确认。
- [ ] 文档：`README.md`、`ARCHITECTURE.md`、`deployment.md`、`system.md`、`features.md`、各模块文档；设计补上「实施中的调整」。
- [ ] 补上本计划的最终结果和验证情况，移到 `plans/completed/`。

## 当前进度与下一步

第 0 阶段已完成（2026-11-20）：设计已采纳，ADR-0008 已写好。核对依赖时，TypeScript 改为锁定 6.0.3（设计第 10 节）。提交 `5966072`（`ui` 分支）。

第 1 阶段已完成（2026-11-21）。实施中的调整见设计第 10 节第 2～8 条。

前端在 `ui` 分支开发，用独立的 worktree `D:\Desktop\Projects\MarsChat-ui`。主目录同时在 `dev` 上进行后端步骤 6（media），两边互不干扰；合并时注意 `Makefile`、`.gitignore`、`AGENTS.md`、`deployment.md` 两边都有改动。

下一步：第 2 阶段（视觉原型）。

## 遇到的问题

- 2026-11-20：调研时 Context7 两次连接失败（`ECONNRESET`），Motion 的 API 改为对照 motion.dev 官方文档核实；直连 `registry.npmjs.org` 超时，设计中的版本号来自 pnpm 当前配置的 npmmirror 镜像。
- 2026-11-20：React Bits 的 WebGL 背景在浏览器截图时没有渲染出来，换成火星红后的实际效果留到第 2 阶段验证。
- 2026-11-21：Vitest 默认把 CSS 替换为空字符串，对比度测试用 `?raw` 读取 `tokens.css` 时拿到空串，报找不到 `:root`。在 `test.css.include` 中对 `tokens.css` 开启处理后正常。
- 2026-11-21：在 Git Bash 里用 curl 访问 `http://localhost:5173` 返回 502，响应头带 `Proxy-Connection`。原因是本机设置了系统 HTTP 代理，请求被发到了代理而不是 Vite。加 `--noproxy '*'` 后正常，已写入 `deployment.md`。
