# 系统架构（当前状态）

> **重构说明（2026-11-18）**：本文描述的是已移除的旧基线（截至提交 `6624cea`），只作参考。新的分层与横切约定在 [ADR-0005](adr/0005-distributed-architecture.md) 中，重构步骤 1 完成后重写本文，见 [后端重构计划](../plans/active/backend-refactor.md)。

> 本文描述**当前代码**的实际结构，以 2026-11-07 首次提交时的代码为准。架构变化时直接更新本文；做出重要技术选型时另写 [ADR](adr/README.md)。

## 1. 部署形态

MarsChat 由一个 Go 后端进程和一个 Vue 3 单页应用组成，前后端分离：

- 后端是**单个进程**，提供 HTTP 业务接口、WebSocket 长连接和静态文件服务。本地模板监听 `127.0.0.1:8000`，`httpEnabled=true` 时使用 HTTP / WS；关闭该开关时使用 HTTPS / WSS。
- 前端构建为静态文件，由独立的 Web 服务器托管（历史部署方式见 [deployment.md](../deployment.md)）。
- 在线连接表保存在进程内存中，因此**当前只支持单实例部署**（详见 [tech-debt.md](../plans/tech-debt.md) TD-07）。

架构总览图见 [ARCHITECTURE.md](../../ARCHITECTURE.md)。

## 2. 后端分层

```
cmd/mars_chat_server/main.go   启动入口：选择消息模式、启动转发服务、RunTLS、处理退出信号
api/v1/                        Controller：绑定 JSON 请求，调用 service，用 JsonBack 统一返回
internal/https_server/         Gin 引擎、中间件、全部路由注册（init 中完成）
internal/service/gorm/         业务服务：用户、联系人、群组、会话、消息、聊天室
internal/service/chat/         WebSocket 连接管理与消息转发（channel / kafka 两种实现）
internal/service/kafka/        kafka-go Writer / Reader 封装
internal/service/redis/        go-redis 封装：读写、按前缀/后缀/模式删除、清空
internal/service/sms/          阿里云短信验证码
internal/service/aes/          AES 加解密工具（当前未被引用）
internal/dao/                  GORM 连接与 AutoMigrate（init 中完成）
internal/model/                6 个 GORM 模型，见 data-model.md
internal/dto/request|respond/  请求与响应结构体（JSON 字段为 snake_case）
internal/config/               TOML 配置加载（单例）
pkg/constants  pkg/enum         常量与状态枚举
pkg/ssl  pkg/zlog  pkg/util     HTTPS 重定向中间件、zap 日志封装、随机数工具
```

依赖方向：`api/v1 → service/* → dao/model/redis/kafka`，`service/chat` 直接访问 `dao` 和 `redis`，不经过 `service/gorm`。

### 初始化顺序

各基础组件都在包的 `init()` 中初始化，导入即生效，没有显式的依赖注入：

1. `config.GetConfig()` 首次调用时读取 `MARSCHAT_CONFIG` 指定的文件，未设置则读取 `../../configs/config_local.toml`（默认从入口目录启动），失败则 `log.Fatal`。
2. `zlog` 初始化 zap，同时输出到 stdout 和 `logConfig.logPath`。
3. `dao` 使用 `mysqlConfig` 完整参数通过 TCP 连接 MySQL，并对 6 张表执行 `AutoMigrate`。
4. `redis` 创建客户端（不做连通性检查）。
5. `https_server` 创建 Gin 引擎并注册路由。
6. `main()`：kafka 模式下初始化 Kafka；启动 `ChatServer` 或 `KafkaChatServer` 协程；根据 `httpEnabled` 使用 `Run` 或 `RunTLS` 监听（仅 HTTPS 模式注册 TLS 重定向）；收到 SIGINT/SIGTERM 后关闭 Kafka、关闭 ChatServer，并**删除当前 Redis 库的全部键**。

本地运行前需要修改的硬编码项见 [deployment.md](../deployment.md#硬编码项清单)。

## 3. 前端结构

| 位置 | 职责 |
|---|---|
| `web/chat-server/src/router/index.js` | 8 个页面路由加根路径重定向；全局守卫只检查 `sessionStorage` 中是否有 `userInfo.uuid` |
| `src/store/index.js` | Vuex：`backendUrl`、`wsUrl`（默认本机 8000，可通过构建环境变量覆盖）、`userInfo`、全局 `socket` |
| `src/views/access/` | 密码登录、短信登录、注册；登录成功后建立 WebSocket |
| `src/views/chat/session/SessionList.vue` | 会话列表 |
| `src/views/chat/contact/ContactList.vue` | 联系人、群组、好友申请 |
| `src/views/chat/contact/ContactChat.vue` | 聊天主页面（约 2800 行）：消息收发、文件、群管理、WebRTC 通话 |
| `src/views/chat/user/OwnInfo.vue` | 个人信息 |
| `src/views/manager/Manager.vue` + `components/*Modal.vue` | 后台管理 |
| `src/App.vue` | 刷新页面后重新拉取用户信息并重建 WebSocket |

前端用 axios 直接调用后端 `POST` 接口，没有封装统一的请求层。

## 4. 关键技术组件

| 组件 | 版本（go.mod / package.json） | 用途 |
|---|---|---|
| Go | 1.27.3 | 后端语言 |
| Gin | v1.10.0 | HTTP 路由；`gin-contrib/cors` 跨域，`unrolled/secure` 做 HTTPS 重定向 |
| GORM + MySQL 驱动 | v1.25.12 / v1.5.7 | ORM，启动时自动迁移 |
| go-redis | v8.11.5 | 列表缓存、短信验证码 |
| gorilla/websocket | v1.5.3 | WSS 长连接 |
| segmentio/kafka-go | v0.4.47 | kafka 模式下的消息中转 |
| zap | v1.27.0 | 结构化日志（JSON） |
| 阿里云 dysmsapi SDK | v4.1.0 | 短信验证码 |
| Vue / Vue Router / Vuex | ^3.2.13 / ^4.0.3 / ^4.0.0 | 前端框架 |
| Element Plus | ^2.9.0 | UI 组件库 |
| WebRTC（浏览器原生） | — | 一对一音视频，信令经 WebSocket 转发 |

前端包管理器为 pnpm 11.22.0（`packageManager` 固定版本，唯一锁文件为 `pnpm-lock.yaml`），构建工具为 Vue CLI 5。迁移决策见 [ADR-0004](adr/0004-frontend-pnpm.md)。

## 5. 横切约定

以下约定来自基线代码的实际实现，新代码应保持一致；若要改变，请先写 ADR。

### 5.1 接口与返回值

- 除 `GET /wss` 外，所有业务接口都是 `POST` + JSON 请求体，没有 RESTful 资源路径。
- HTTP 状态码恒为 200，业务结果放在响应体中：`{"code": 200|400|500, "message": "...", "data": ...}`。
- Service 方法返回 `(message, [data,] ret)`，由 `api/v1/controller.go` 的 `JsonBack` 转换：

| ret | 含义 | code | message 内容 |
|---|---|---|---|
| `0` | 成功 | 200 | 成功提示 |
| `-2` | 业务错误（如用户被禁用、验证码错误） | 400 | 给用户看的提示 |
| `-1` | 系统错误（数据库、Redis 等异常） | 500 | 固定为 `constants.SYSTEM_ERROR` |

- 系统错误的细节只写日志，不返回前端。业务与系统错误都用 `zlog.Error` 记录。决策背景见 [ADR-0001](adr/0001-service-return-convention.md)。

### 5.2 ID 规则

业务 ID 为 `前缀 + yyyyMMdd + 11 位随机数`（`pkg/util/random`），前缀决定实体类型，代码中大量用首字母判断类型：

| 前缀 | 实体 | 前缀 | 实体 |
|---|---|---|---|
| `U` | 用户 | `S` | 会话 |
| `G` | 群聊 | `M` | 消息 |
| `A` | 申请记录 | | |

### 5.3 静态资源路径

- 头像和文件保存在 `staticSrcConfig` 指定的目录，经 `/static/avatars`、`/static/files` 公开访问。
- 数据库中的头像存**相对路径** `/static/avatars/<文件名>`；前端展示时若不以 `http` 开头就拼上 `backendUrl`。默认头像是一个外部 CDN 地址。
- 写入消息前，转发服务用 `normalizePath` 去掉发送者头像中的服务器地址前缀（背景见 [lessons/static-path-normalization.md](../lessons/static-path-normalization.md)）。
- 例外：文件消息的 `url` 字段目前存的是带服务器地址的绝对 URL（TD-10）。

### 5.4 删除策略

用户、群组、会话、联系关系、申请记录全部使用 GORM 软删除（`deleted_at`）。管理端的用户与群组列表用 `Unscoped()` 查询以显示已删除记录。

### 5.5 日志

- `zlog.Info/Warn/Error/Fatal` 会附带调用方函数名、文件和行号。
- 代码中还混用了 `log.Println` 和 `fmt.Println`，新代码统一用 `zlog`。

## 6. 模块与代码位置索引

| 业务模块 | 文档 | 后端 | 前端 |
|---|---|---|---|
| 用户与认证 | [user-auth.md](../product/modules/user-auth.md) | `user_info_service.go`、`sms/` | `views/access/`、`OwnInfo.vue` |
| 联系人与申请 | [contact.md](../product/modules/contact.md) | `user_contact_service.go` | `ContactList.vue`、`ContactListModal.vue` |
| 群组 | [group.md](../product/modules/group.md) | `group_info_service.go` | `ContactChat.vue`、`ContactListModal.vue` |
| 会话与消息 | [chat-message.md](../product/modules/chat-message.md) | `session_service.go`、`message_service.go`、`service/chat/` | `SessionList.vue`、`ContactChat.vue` |
| 音视频通话 | [av-call.md](../product/modules/av-call.md) | `service/chat/`（只转发） | `ContactChat.vue` |
| 后台管理 | [admin.md](../product/modules/admin.md) | `user_info_service.go`、`group_info_service.go` | `Manager.vue`、`components/*Modal.vue` |

数据模型、枚举和 Redis 键见 [data-model.md](data-model.md)；请求与消息的流转过程见 [data-flow.md](data-flow.md)。
