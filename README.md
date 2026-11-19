# MarsChat

MarsChat 是一个前后端分离的 Web 即时通讯系统，提供联系人、单聊群聊、文件传输、一对一音视频通话和后台管理。后端为 Go 单体服务，前端为 Vue 3 单页应用。

> **重构说明（2026-11-18）**：除“快速开始”和“开发检查”两节已按新后端更新外，本文描述的是已移除的旧基线（截至提交 `6624cea`）。后端正在 `dev` 分支按 [后端重构计划](docs/plans/active/backend-refactor.md) 用 Kratos v3 重写为微服务，前端之后用 React 重写；目标架构见 [ADR-0005](docs/architecture/adr/0005-distributed-architecture.md)。本文将在重构完成时重写。

## 核心功能

- 手机号 + 短信验证码注册，密码或验证码登录
- 好友与群的申请、审批、拉黑、删除
- 群聊管理：建群、改资料、移除成员、退群、解散
- 基于 WebSocket 的单聊、群聊实时消息与文件消息，离线消息落库
- 基于 WebRTC 的单聊音视频通话
- 管理员启用 / 禁用 / 删除用户和群聊，设置管理员

各功能的实现状态见 [docs/product/features.md](docs/product/features.md)。

## 技术栈

| 层 | 技术 |
|---|---|
| 后端 | Go 1.27.3、Gin、GORM、go-redis、gorilla/websocket、kafka-go（可选）、zap |
| 前端 | Vue 3、Vue Router、Vuex、Element Plus、axios、Vue CLI 5、pnpm |
| 存储与中间件 | MySQL、Redis、Kafka（可选） |
| 外部服务 | 阿里云短信；TURN 服务（公网音视频，可选） |

## 目录结构

```
cmd/mars_chat_server/   后端入口 main.go
api/v1/                 HTTP / WebSocket 控制器
internal/               配置、数据访问、模型、DTO、业务服务、聊天转发
pkg/                    常量、枚举、日志、HTTPS 中间件、工具
configs/config.toml     配置模板
test/                   测试（目前无法运行，见 TD-12）
web/chat-server/        Vue 3 前端
docs/                   项目文档
```

## 快速开始

重构中的后端需要 Go 1.27.3、Docker Desktop 和 buf 1.71.0，在 Git Bash 中执行：

```bash
cp .env.example .env    # 填入本地密码
make up                 # 启动 MySQL、Redis、Kafka、Consul
make run-account        # 另开终端运行 make run-social
```

完整步骤、端口和配置说明见 [deployment.md](docs/deployment.md)。前端尚未重写，暂无启动方式。

## 开发检查

```bash
make lint vet test build
```

## 文档

| 文档 | 内容 |
|---|---|
| [ARCHITECTURE.md](ARCHITECTURE.md) | 架构总览 |
| [docs/README.md](docs/README.md) | 文档索引与编写规则 |
| [docs/product/overview.md](docs/product/overview.md) | 产品定位与系统边界 |
| [docs/deployment.md](docs/deployment.md) | 部署与配置 |
| [AGENTS.md](AGENTS.md) | Coding Agent 工作指南 |

## 许可证

[MIT License](LICENSE)
