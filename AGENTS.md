# AGENTS.md

面向 Coding Agent 的工作指南。这里只放导航和规则，项目知识写在 `docs/` 中，不要复制到本文件。

## 项目速览

- MarsChat：Web 即时通讯系统。后端 Go 单体（`cmd/mars_chat_server`，模块名 `mars_chat_server`），前端 Vue 3（`web/chat-server`）。
- 依赖 MySQL、Redis，可选 Kafka；HTTPS / WSS；WebRTC 一对一通话。
- 当前是改造前的基线版本，已知问题集中记录在 [docs/plans/tech-debt.md](docs/plans/tech-debt.md)。

## 文档导航

| 任务涉及 | 先读 |
|---|---|
| 任何任务 | [ARCHITECTURE.md](ARCHITECTURE.md)、[docs/README.md](docs/README.md) |
| 某个业务功能 | [docs/product/features.md](docs/product/features.md) → `docs/product/modules/` 下对应模块文档 |
| 表、状态值、Redis 键 | [docs/architecture/data-model.md](docs/architecture/data-model.md) |
| WebSocket、消息转发、通话信令 | [docs/architecture/data-flow.md](docs/architecture/data-flow.md)，以及 [internal/service/chat/AGENTS.md](internal/service/chat/AGENTS.md) |
| 分层、约定、技术组件 | [docs/architecture/system.md](docs/architecture/system.md) |
| 设计取舍 | [docs/architecture/adr/](docs/architecture/adr/README.md) |
| 运行、配置、部署 | [docs/deployment.md](docs/deployment.md) |
| 进行中的复杂任务 | `docs/plans/active/`（如存在） |

## 工作规则

1. **开始前**：根据任务范围阅读相关文档和代码；涉及特定模块时先读该模块的功能与架构说明；检查 `docs/plans/active/` 是否有未完成的计划。
2. **先核实再动手**：文档与代码不一致时，先确认实际行为，再修正文档或代码，不能盲目相信任何一方。
3. **重大改动先提方案**：架构改造、技术选型变更、跨模块重构、数据库结构变更、接口协议变更，先以“提议中”状态写 ADR 或设计文档，等开发者确认后再实施。
4. **复杂任务维护计划**：多步骤或跨会话的任务，在 `docs/plans/active/` 建立计划并随进度更新；完成后移到 `docs/plans/completed/`。
5. **完成后汇报**：说明修改内容、验证命令与结果、文档更新情况、遗留问题。验证没跑或失败要如实说明。

## 开发约束

- 新代码遵循 [system.md 横切约定](docs/architecture/system.md#5-横切约定)：Service 返回 `(message, data, ret)`，Controller 用 `JsonBack`；ID 用类型前缀；静态资源存相对路径；删除用软删除；日志用 `zlog`。
- 代码注释沿用仓库现有的中文。
- 修改 `internal/service/chat/` 前必须读该目录下的 AGENTS.md：消息处理逻辑在 `server.go` 和 `kafka_server.go` 中各有一份，需要同步修改。
- WebSocket 消息格式或通话信令变化时，后端 `service/chat`、前端 `ContactChat.vue` 和 `data-flow.md` 必须同时更新。
- 配置路径、数据库连接、证书路径、前端后端地址是硬编码的（[清单](docs/deployment.md#硬编码项清单)）。不要把本机路径、IP、密钥提交进仓库；真实配置放在被忽略的 `config_local.toml` 中。
- 前端包管理器是 yarn；后端依赖用 go mod。新增、升级依赖或修改 `go.mod`、`package.json` 前先征得开发者同意。

## 验证要求

改完后按顺序运行能跑的最轻量检查：

```bash
go vet ./cmd/... ./internal/... ./pkg/... ./api/...   # 不要加 -mod=mod，它会改写 go.mod
go build ./cmd/mars_chat_server
cd web/chat-server && yarn lint                       # 需要已安装 node_modules
```

- `go vet ./test/...` 目前因 `test/dao` 字段过期而失败（TD-12），这是已知问题，不代表本次改动引入错误；修复后删除本条。
- 现有测试依赖真实 MySQL、Redis 和固定路径的配置文件，无法在普通开发环境运行。新增测试应尽量不依赖外部服务。
- 不为了通过检查而修改测试预期、跳过断言或吞掉错误。

## 文档同步

| 变化 | 需要更新 |
|---|---|
| 新增、修改、删除功能 | `docs/product/features.md` 和对应模块文档 |
| 接口增删或字段变化 | 对应模块文档的接口表 |
| 表结构、枚举、Redis 键变化 | `docs/architecture/data-model.md` |
| 分层、组件、约定变化 | `docs/architecture/system.md`、`ARCHITECTURE.md`；重要选型新增 ADR，旧 ADR 标注“已被替代”而不是删除 |
| 消息流转或协议变化 | `docs/architecture/data-flow.md` |
| 配置项、部署方式变化 | `docs/deployment.md` |
| 修复或新发现技术债 | `docs/plans/tech-debt.md`（编号不复用） |
| 解决了难定位的问题 | 在 `docs/lessons/` 新增经验文档 |

文档写法见 [docs/README.md 编写规则](docs/README.md#编写规则)。
