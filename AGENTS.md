# AGENTS.md

面向 Coding Agent 的工作指南。这里只放导航和规则，项目知识写在 `docs/` 中，不要复制到本文件。

## 项目速览

- MarsChat：Web 即时通讯系统，代码在 `dev` 分支。后端是 Kratos v3 微服务（access、account、social、message、delivery、media），接口用 protobuf 定义，2026-11-21 按后端重构计划完成；前端在 `web/` 用 React + pnpm 重写，已接入认证（登录、注册、令牌刷新、退出）、实时消息（文字消息、已读、多端同步、断线补拉）、联系人与群组（好友、申请、黑名单、建群、成员管理、实时通知）、设置与管理（资料、登录设备、外观、封禁）、文件（图片与文件消息、上传进度、头像）和一对一音视频通话（TURN 中继尚未部署，只能在可以直连的网络中接通）。
- 架构总览见 [ARCHITECTURE.md](ARCHITECTURE.md)，选型见 [ADR-0005](docs/architecture/adr/0005-distributed-architecture.md)、[ADR-0006](docs/architecture/adr/0006-message-sync-model.md) 和 [架构图](docs/images/proposed-distributed-architecture.svg)；后端重构的过程、实测数据与遗留项见 [docs/plans/completed/backend-refactor.md](docs/plans/completed/backend-refactor.md)。
- Go module 为 `github.com/Mars-08042/MarsChat`。依赖组件由 Compose 提供：MySQL、Redis、Kafka、Consul、APISIX；监控组件（Prometheus、Alertmanager、Grafana、Jaeger）和压测工具 k6 在单独的 profile 中，见 [deployment.md](docs/deployment.md)。文件存储默认用仓库下的本地目录，可切换为阿里云 OSS（[ADR-0007](docs/architecture/adr/0007-object-storage.md)）。
- 文档只描述当前代码。旧版本（Gin 单体 + Vue 前端）的描述已删除，需要时查看提交 `6624cea`；设计文档与 ADR 中提到的旧基线只是决策背景。

## 文档导航

| 任务涉及 | 先读 |
|---|---|
| 任何任务 | [docs/README.md](docs/README.md)、`docs/plans/active/` 中进行中的计划；后端的来龙去脉见 [后端重构计划](docs/plans/completed/backend-refactor.md) |
| 架构与选型 | [ADR-0005](docs/architecture/adr/0005-distributed-architecture.md)、[ADR 索引](docs/architecture/adr/README.md) |
| 消息、WebSocket、多端同步、通话信令 | [ADR-0006](docs/architecture/adr/0006-message-sync-model.md)；[data-flow.md](docs/architecture/data-flow.md) |
| 某个业务功能 | [features.md](docs/product/features.md) → `docs/product/modules/` |
| 表、索引、Redis 键 | [data-model.md](docs/architecture/data-model.md)，设计原则见重构计划 |
| 分层与横切约定 | [system.md](docs/architecture/system.md) |
| 运行、配置、部署 | [deployment.md](docs/deployment.md)，完整端口表见[重构计划](docs/plans/completed/backend-refactor.md#端口) |
| 前端与 UI | [前端设计](docs/design/frontend.md)、[前端计划](docs/plans/active/frontend-rewrite.md)、[ADR-0008](docs/architecture/adr/0008-react-frontend.md) |
| 参考实现 | `tmp/teamgram-server/`（只读参考，被 git 忽略），对照清单见重构计划。需要阅读其他第三方源码时同样克隆到 `tmp/`，并切到 `go.mod` 中使用的版本，例如 `tmp/kratos` |

## 工作规则

1. **开始前**：根据任务范围阅读相关文档和代码；涉及特定模块时先读该模块的功能与架构说明；检查 `docs/plans/active/` 是否有未完成的计划。
2. **先核实再动手**：文档与代码不一致时，先确认实际行为，再修正文档或代码，不能盲目相信任何一方。
3. **重大改动先提方案**：架构改造、技术选型变更、跨模块重构、数据库结构变更、接口协议变更，先以“提议中”状态写 ADR 或设计文档，等开发者确认后再实施。
4. **复杂任务维护计划**：多步骤或跨会话的任务，在 `docs/plans/active/` 建立计划并随进度更新；完成后移到 `docs/plans/completed/`。
5. **完成后汇报**：说明修改内容、验证命令与结果、文档更新情况、遗留问题。验证没跑或失败要如实说明。

## 开发约束

- 新代码遵循 ADR-0005 的约定：接口契约写在 `api/<服务>/v1/*.proto`，服务代码按 Kratos v3 分层放在 `app/<服务>/`，跨服务公共代码放 `pkg/`；业务错误用 Kratos errors；日志用 `slog`。细则以 `system.md` 为准。
- 数据库与缓存遵循重构计划中的“数据库与缓存设计原则”。表结构变更写 SQL 迁移，不用 AutoMigrate。
- 代码注释用中文。
- 容器宿主机端口为默认端口 + 10000，只绑定 `127.0.0.1`；应用服务 HTTP 用 `18xx0`、gRPC 用 `19xx0`。完整端口表见重构计划。
- 只操作 MarsChat 自己的容器、卷和镜像。本机还有其他项目的容器，例如 `pestle-redis` 使用 `redis:7` 镜像。
- 不要把本机路径、IP、密钥提交进仓库；真实配置放在被忽略的本地文件中。
- `tmp/` 只放临时文件和只读参考源码，可以随时清空；需要保留的本地数据放在 `data/` 的子目录（都被 Git 忽略）：`data/media` 文件存储、`data/load` 压测数据、`data/emoji-sources` 表情来源仓库。
- 后端依赖用 go mod。新增、升级依赖或修改 `go.mod` 前先征得开发者同意（重构计划中已列出的除外）。
- 前端在 `web/`，依赖用 pnpm；新增或升级依赖同样先征得同意（[前端设计](docs/design/frontend.md) 第 3.1 节已列出的除外）。设计变量在 `web/src/styles/tokens.css`，改颜色后要通过对比度测试；第三方源码按设计 3.3 节登记到 `web/THIRD_PARTY_NOTICES.md`。
- WebSocket 帧或通话信令变化时，同步更新对应的 proto 定义和 `data-flow.md`。

## 验证要求

改完后按顺序运行能跑的最轻量检查：

```bash
buf lint                 # 或 make lint
go vet ./...             # 不要加 -mod=mod，它会改写 go.mod
go build ./...
go test ./...
```

- 改了前端代码后运行 `make web-check`（lint → 类型检查 → 单元测试 → 构建）。
- 改了 proto 或 `wire.go` 后先 `make gen`（会一并生成前端 TS 代码，需要先 `make web-install`），生成代码与源文件一起提交，CI 会检查两者一致。
- `tmp/teamgram-server` 有自己的 `go.mod`，不在 `./...` 范围内。
- 单元测试尽量不依赖外部服务；需要 Compose 环境的端到端测试放在 `test/e2e`（构建标签 `e2e`，用 `make e2e` 运行）。
- 不为了通过检查而修改测试预期、跳过断言或吞掉错误。

## 文档同步

| 变化 | 需要更新 |
|---|---|
| 新增、修改、删除功能 | `docs/product/features.md` 和对应模块文档 |
| 接口增删或字段变化 | proto 文件与对应模块文档的接口表 |
| 表结构、索引、Redis 键变化 | `docs/architecture/data-model.md` 与 SQL 迁移 |
| 分层、组件、约定变化 | `docs/architecture/system.md`、`ARCHITECTURE.md`；重要选型新增 ADR，旧 ADR 标注“已被替代”而不是删除 |
| 消息流转或协议变化 | `docs/architecture/data-flow.md` |
| 配置项、端口、部署方式变化 | `docs/deployment.md` |
| 计划进展 | `docs/plans/active/` 中对应的计划，完成后移到 `docs/plans/completed/` |
| 修复或新发现技术债 | `docs/plans/tech-debt.md`（编号不复用，新问题接着文件中最大的编号往后排） |
| 解决了难定位的问题 | 在 `docs/lessons/` 新增经验文档 |

文档写法见 [docs/README.md 编写规则](docs/README.md#编写规则)。
