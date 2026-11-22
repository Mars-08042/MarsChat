# 文档索引

文档描述 `dev` 分支上的当前代码：Kratos v3 微服务后端已按 [后端重构计划](plans/completed/backend-refactor.md) 完成（2026-11-21），前端在 `web/` 重写中，见[前端计划](plans/active/frontend-rewrite.md)。旧版本（Gin 单体 + Vue 3 前端）的代码与文档见提交 `6624cea`。

## 目录

```
docs/
├── product/                 项目是什么、实现了哪些功能
│   ├── overview.md          定位、角色、业务场景、系统边界
│   ├── features.md          功能清单与实现状态（权威）
│   └── modules/             各业务模块：职责、流程、接口、代码位置
├── architecture/            系统现在如何工作、为什么这样设计
│   ├── system.md            分层、目录、技术组件、横切约定
│   ├── data-model.md        表结构、Redis 键
│   ├── data-flow.md         请求、消息、事件、信令的流转与 WebSocket 帧格式
│   └── adr/                 架构决策记录
├── design/                  各服务与重大改造的设计文档（含实施中的调整）
├── deployment.md            运行依赖、依赖组件、配置、开发命令、监控与压测
├── plans/                   正在做什么、做完了什么、还有什么问题
│   ├── active/              进行中的计划
│   ├── completed/           已完成的计划
│   └── tech-debt.md         技术债与已知问题
├── lessons/                 踩过的坑与预防措施
└── images/                  文档插图（SVG）
```

已完成计划：[后端重构](plans/completed/backend-refactor.md)（含故障实验与压测结果）；旧基线时期的 [本地 Compose 开发环境](plans/completed/local-compose.md)、[无证书本地启动](plans/completed/local-http.md)、[前端迁移 pnpm](plans/completed/pnpm-migration.md)。

## 按问题查找

| 我想知道 | 去看 |
|---|---|
| 怎么跑起来 | [../README.md](../README.md)、[deployment.md](deployment.md) |
| 某个功能做没做、做到什么程度 | [product/features.md](product/features.md) |
| 某个模块的接口和代码在哪 | [product/modules/](product/modules/) 下对应文档 |
| 整体架构 | [../ARCHITECTURE.md](../ARCHITECTURE.md) → [architecture/system.md](architecture/system.md) |
| 某张表、某个缓存键 | [architecture/data-model.md](architecture/data-model.md) |
| WebSocket 帧格式、通话信令、事件 | [architecture/data-flow.md](architecture/data-flow.md) |
| 为什么这样设计 | [architecture/adr/](architecture/adr/README.md)、[design/](design/) |
| 可靠性与性能的实测数据 | [后端重构计划的故障实验与压测结果](plans/completed/backend-refactor.md#故障实验结果) |
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
| [kratos-grpc-selector-nil.md](lessons/kratos-grpc-selector-nil.md) | Kratos v3.0.0 的 gRPC 负载均衡器在 init 时存下了空的全局选择器，经服务发现调用会 panic |
| [docker-desktop-bind-mount-hang.md](lessons/docker-desktop-bind-mount-hang.md) | Hyper-V 后端的 Docker Desktop 挂载未授权目录时，compose 会一直等待共享确认 |
| [kratos-v3-server-defaults.md](lessons/kratos-v3-server-defaults.md) | Kratos 自带的日志与恢复中间件会把密码等请求体写进日志、内部错误原文返回客户端；v3 的 json codec 不按 protobuf JSON 收发 |
| [redis-resp3-reply-types.md](lessons/redis-resp3-reply-types.md) | go-redis v9 默认 RESP3，`BF.EXISTS` 返回布尔值，按整数读取会让布隆过滤器静默失效 |
| [franz-go-record-timestamp-timeout.md](lessons/franz-go-record-timestamp-timeout.md) | franz-go 按记录的 Timestamp 计算投递超时，填事件创建时间会让积压的 Outbox 事件一发出就超时 |
| [redis-dial-stall-docker-dns.md](lessons/redis-dial-stall-docker-dns.md) | Redis 容器停止后，go-redis 的拨号重试把每条命令拖到几秒，本该放行的调用变成请求超时 |
| [smtp-465-tls-interception.md](lessons/smtp-465-tls-interception.md) | 开发机到 465 端口的 TLS 连接被截获，SMTP 发信报证书不受信任；改用 587（STARTTLS），不要关闭证书校验 |
| [innodb-gap-lock-register-deadlock.md](lessons/innodb-gap-lock-register-deadlock.md) | 注册事务中查不到行的 `SELECT ... FOR UPDATE` 锁住索引末尾的间隙，新用户 ID 递增，并发注册互相死锁 |
| [race-ci-test-timing.md](lessons/race-ci-test-timing.md) | 竞争检测器看不到经 TCP 传递的先后关系；服务端先回复再推送，测试把回复当完成信号，在 CI 的 `-race` 下偶发失败 |
| [consul-no-leader-resolver.md](lessons/consul-no-leader-resolver.md) | 服务在 Consul 选出 leader 之前启动，第一次服务发现查询失败后 contrib 留下空的登记，到下游的 gRPC 调用一直超时，直到重启 |

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
