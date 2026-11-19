# 文档索引

> **重构说明（2026-11-18）**：后端正在 `dev` 分支按 [后端重构计划](plans/active/backend-refactor.md) 重写，目标架构见 [ADR-0005](architecture/adr/0005-distributed-architecture.md)、[ADR-0006](architecture/adr/0006-message-sync-model.md)。`system.md`、`data-model.md`、`deployment.md` 已按新后端重写（步骤 1）；其余 `product/`、`architecture/`（ADR 除外）文档仍描述已移除的旧基线，将按重构步骤逐篇重写。

## 目录

```
docs/
├── product/                 项目是什么、实现了哪些功能
│   ├── overview.md          定位、角色、业务场景、系统边界
│   ├── features.md          功能清单与实现状态（权威）
│   └── modules/             各业务模块：职责、流程、接口、代码位置
├── architecture/            系统现在如何工作、为什么这样设计
│   ├── system.md            分层、目录、技术组件、横切约定
│   ├── data-model.md        表结构、枚举、Redis 键
│   ├── data-flow.md         请求、消息、信令的流转与消息格式
│   └── adr/                 架构决策记录
├── deployment.md            运行依赖、依赖组件、配置、开发命令
├── plans/                   正在做什么、还有什么问题
│   └── tech-debt.md         技术债与已知问题
├── lessons/                 踩过的坑与预防措施
└── images/                  文档插图（SVG）
```

`design/`、`plans/active/` 按需创建。已完成计划包括 [本地 Compose 开发环境](plans/completed/local-compose.md)、[无证书本地启动](plans/completed/local-http.md)、[前端迁移 pnpm](plans/completed/pnpm-migration.md)。

## 按问题查找

| 我想知道 | 去看 |
|---|---|
| 怎么跑起来 | [../README.md](../README.md)、[deployment.md](deployment.md) |
| 某个功能做没做、做到什么程度 | [product/features.md](product/features.md) |
| 某个模块的接口和代码在哪 | [product/modules/](product/modules/) 下对应文档 |
| 整体架构 | [../ARCHITECTURE.md](../ARCHITECTURE.md) → [architecture/system.md](architecture/system.md) |
| 某张表、某个状态值、某个缓存键 | [architecture/data-model.md](architecture/data-model.md) |
| WebSocket 消息格式、通话信令 | [architecture/data-flow.md](architecture/data-flow.md) |
| 为什么这样设计 | [architecture/adr/](architecture/adr/README.md) |
| 有哪些已知缺陷 | [plans/tech-debt.md](plans/tech-debt.md) |

## 模块文档

| 模块 | 文档 |
|---|---|
| 用户与认证 | [user-auth.md](product/modules/user-auth.md) |
| 联系人与申请 | [contact.md](product/modules/contact.md) |
| 群组 | [group.md](product/modules/group.md) |
| 会话与消息 | [chat-message.md](product/modules/chat-message.md) |
| 音视频通话 | [av-call.md](product/modules/av-call.md) |
| 后台管理 | [admin.md](product/modules/admin.md) |

## 工程经验

| 文档 | 摘要 |
|---|---|
| [av-signaling-no-echo.md](lessons/av-signaling-no-echo.md) | 通话信令回显给发送方会导致自己收到自己的通话请求 |
| [static-path-normalization.md](lessons/static-path-normalization.md) | 静态资源地址存完整 URL，换服务器地址后全部失效 |
| [kratos-grpc-selector-nil.md](lessons/kratos-grpc-selector-nil.md) | Kratos v3.0.0 的 gRPC 负载均衡器在 init 时存下了空的全局选择器，经服务发现调用会 panic |
| [docker-desktop-bind-mount-hang.md](lessons/docker-desktop-bind-mount-hang.md) | Hyper-V 后端的 Docker Desktop 挂载未授权目录时，compose 会一直等待共享确认 |
| [kratos-v3-server-defaults.md](lessons/kratos-v3-server-defaults.md) | Kratos 自带的日志与恢复中间件会把密码等请求体写进日志、内部错误原文返回客户端；v3 的 json codec 不按 protobuf JSON 收发 |

## 编写规则

- **一类知识只在一处权威记录**，其他文档用相对链接引用：功能状态在 `features.md`，表与状态值在 `data-model.md`，消息格式在 `data-flow.md`，已知问题在 `tech-debt.md`（用 TD 编号引用），部署配置在 `deployment.md`。
- 以代码实际行为为准；不确定的内容写明“未验证”或“待确认”，不推测。
- 用中文编写，代码标识符、路径、命令保持原样。
- 文档文件名使用描述主题的短横线命名，不添加日期或时间戳前缀；日期记录在正文元信息中，ADR 保留顺序编号。
- 插图放在 `images/`，用手写 SVG，风格与现有图一致：白底，`#b4b2a9` 灰色圆角框，`#2c6fd1` 浅蓝高亮重点，`#ecebe6` 注释框，标题用一句结论。

### 设计文档

只为有一定复杂度的功能或重大改造写设计文档，放在 `design/主题.md`，包含：问题背景与目标、需求与约束、方案与模块设计、数据结构与接口、候选方案与权衡、风险与验证方案。

### 计划文档

复杂任务在 `plans/active/主题.md` 建立计划，便于切换会话后恢复上下文：

```markdown
# 计划：主题
- 状态：进行中 | 开始：YYYY-MM-DD | 关联设计：链接
## 目标
## 步骤
- [x] 已完成的步骤（附提交）
- [ ] 未完成的步骤
## 当前进度与下一步
## 遇到的问题
```

完成后把文件移到 `plans/completed/`，补上最终结果和验证情况。

### 经验文档

在 `lessons/` 下新建，每篇包含：现象、根因、解决方案、预防措施，并注明相关代码位置或提交。
