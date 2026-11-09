# MarsChat

MarsChat 是一个前后端分离的 Web 即时通讯系统，提供联系人、单聊群聊、文件传输、一对一音视频通话和后台管理。后端为 Go 单体服务，前端为 Vue 3 单页应用。

> 当前代码是后续改造的基线版本，存在若干已知的安全与稳定性问题（例如后端没有认证鉴权），**不适合直接用于生产环境**。详见 [docs/plans/tech-debt.md](docs/plans/tech-debt.md)。

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

需要 Go 1.27.3、MySQL、Redis、Node.js 与 pnpm 11.22.0。本地使用 HTTP / WS，无需证书。

本地开发使用根目录 `compose.yaml` 启动 MySQL、Redis、Kafka，复用已有镜像并禁止自动拉取。宿主机端口分别为 `13307`、`16381`、`19093`。首次运行请按 [本地开发步骤](docs/deployment.md#本地开发windows--powershell) 准备 `.env`、`configs/config_local.toml`（`httpEnabled = true`）和日志、上传目录，再执行：

```powershell
# 在项目根目录启动依赖
docker compose up -d --pull never --wait --wait-timeout 120

# 后端（在入口目录运行，静态文件和日志按相对路径解析）
Set-Location cmd/mars_chat_server
go run .

# 前端（另开终端，从项目根目录执行）
Set-Location web/chat-server
pnpm install --frozen-lockfile
pnpm serve
```

访问 `http://localhost:8080`；后端为 `http://127.0.0.1:8000`。切回 HTTPS 的方法见部署文档。

## 开发检查

```bash
go vet ./cmd/... ./internal/... ./pkg/... ./api/...
go build ./cmd/mars_chat_server
```

`test/` 下的测试目前无法编译，且依赖真实的数据库与固定路径的配置文件。

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
