# MarsChat

MarsChat 是一个 Web 即时通讯系统。后端用 Go + Kratos v3 拆分为六个微服务，实现了可靠消息（幂等、会话内有序、事务 Outbox、断线补拉）、多端同步、跨节点投递和慢连接隔离，并用端到端测试、故障实验和压测验证。前端正在 `web/` 用 React + Vite + pnpm 重写，已完成视觉原型，接入了认证（登录、注册、令牌刷新、退出）、实时消息（会话列表、单聊与群聊的文字消息、已读回执、多端同步、断线补拉）和联系人与群组（好友、申请、黑名单、建群、成员管理、实时通知），设置、文件、通话等尚未接入后端。

> 当前代码在 `dev` 分支。旧版本（Gin 单体 + Vue 3 前端）已在重构中移除，需要时查看提交 `6624cea`。

## 功能

- 邮箱验证码注册，密码或验证码登录；访问令牌与刷新令牌轮换，多设备管理，退出、踢设备、封禁后立即失效
- 好友申请与审批、拉黑；群聊的创建、加入审批、邀请、成员角色、禁言、转让、解散
- WebSocket 单聊、群聊：文本、图片、文件消息；未读数、已读回执、多端同步、断线补拉
- 文件上传与缩略图，限时签名下载地址；存储可切换为本地目录或阿里云 OSS
- 一对一音视频通话信令（媒体为 WebRTC 点对点），多端响铃、接听互斥
- 指标、告警、链路追踪和慢查询日志；k6 压测与故障实验

各功能的实现状态与验证情况见 [features.md](docs/product/features.md)。

## 技术栈

| 层 | 技术 |
|---|---|
| 服务 | Go 1.27.3、Kratos v3（HTTP + gRPC，protobuf 契约）、wire、GORM、go-redis、franz-go、gorilla/websocket |
| 前端 | React、TypeScript、Vite、Tailwind CSS、shadcn/ui、TanStack Query、zustand、Vitest，pnpm 管理依赖（[ADR-0008](docs/architecture/adr/0008-react-frontend.md)） |
| 组件 | MySQL、Redis 8（含布隆过滤器）、Kafka、Consul、Apache APISIX，全部由 Docker Compose 提供 |
| 可观测 | OpenTelemetry、Prometheus、Alertmanager、Grafana、Jaeger；压测用 k6 |
| 存储 | 本地目录或阿里云 OSS |
| 工具 | buf、golang-migrate、GitHub Actions |

## 架构

![当前架构](docs/images/proposed-distributed-architecture.svg)

服务划分、数据流和可靠性设计见 [ARCHITECTURE.md](ARCHITECTURE.md)。

## 目录结构

```
app/<服务>/        access、account、social、message、delivery、media，按 Kratos 分层（server → service → biz → data）
api/<服务>/v1/     接口契约 *.proto 与生成代码
pkg/              跨服务公共组件：启动、鉴权、ID、缓存、布隆过滤器、限流、Outbox、Kafka、遥测等
deploy/           MySQL、APISIX、Prometheus、Alertmanager、Grafana、Jaeger 的配置与初始化脚本
test/             e2e（需要 Compose 环境）、cluster（容器模式的集群测试与故障实验）、load（k6 压测）
web/              前端（React + Vite + pnpm），src/gen/ 为 buf 从 proto 生成的 TS 代码
docs/             项目文档
```

完整说明见 [system.md](docs/architecture/system.md#2-仓库结构)。

## 快速开始

需要 Go 1.27.3、Docker Desktop、buf 1.71.0，在 Git Bash 中执行：

```bash
cp .env.example .env    # 把 change-me 换成随机密码
make keys               # 生成 JWT 密钥与下载地址签名密钥，写入 .env
make up                 # 启动 MySQL、Redis、Kafka、Consul
make migrate-account migrate-social migrate-message migrate-media
make images             # 构建服务镜像
make cluster-up         # 容器模式：多实例服务 + 网关，入口 https://127.0.0.1:19443
```

也可以在宿主机逐个运行服务（`make run-account` 等，每个服务一个终端）。两种方式不能同时运行。

前端需要 Node.js 与 pnpm：

```bash
make web-install        # 按锁文件安装依赖
make web-dev            # 开发服务器 http://localhost:5173，/v1 与 /ws 默认代理到网关
```端口、配置、监控与压测见 [deployment.md](docs/deployment.md)。

## 开发检查

```bash
make lint vet test build   # buf lint、go vet、单元测试、构建
make web-check            # 前端：lint、类型检查、单元测试、构建
make e2e                   # 端到端测试，需要先启动依赖并在宿主机运行全部服务
make cluster-test          # 集群测试与故障实验，需要先 make cluster-up
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
