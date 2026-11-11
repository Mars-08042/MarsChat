# 部署与运行

> 本地 Compose 环境于 2026-11-08 配置；验证结果见本地开发章节。云服务器基线流程未重新验证。

## 运行依赖

| 组件 | 是否必需 | 说明 |
|---|---|---|
| Go | 构建必需 | go.mod 要求 1.27.3 |
| MySQL | 必需 | 启动时自动建表；Compose 首次初始化创建 `marschat` 数据库 |
| Redis | 必需 | 缓存与短信验证码 |
| TLS 证书 | HTTPS 模式必需 | 本地默认 HTTP / WS，无需证书；部署可切回 HTTPS / WSS |
| 阿里云短信 | 注册与验证码登录必需 | 需要 AccessKey；签名和模板目前写死在代码中（TD-13） |
| Node.js + pnpm | 前端构建必需 | 使用 pnpm 11.22.0，版本见 packageManager；本机 Node 24.12.0，开发者已确认可启动并登录 |
| Web 服务器 | 部署前端必需 | 基线流程使用 Apache2 托管 `dist/` 并开启 SSL |
| Kafka | 可选 | 仅 `messageMode = "kafka"` 时需要；topic 需手动预建 |
| TURN 服务 | 可选 | 公网音视频通话需要，基线流程使用 coturn |

## 配置文件

模板为 [configs/config.toml](../configs/config.toml)。从 `cmd/mars_chat_server` 启动时，默认读取 `../../configs/config_local.toml`（被 Git 忽略）；可以通过进程环境变量 `MARSCHAT_CONFIG` 指定其他路径。配置中的相对路径均相对于进程工作目录，而非配置文件目录。

| 配置项 | 是否生效 | 说明 |
|---|---|---|
| `mainConfig.appName` | 是 | 应用名称，数据库名由 `mysqlConfig.databaseName` 单独指定 |
| `mainConfig.host` / `port` | 是 | 监听地址，也用作 HTTPS 重定向的目标主机 |
| `mainConfig.httpEnabled` | 是 | `true` 使用 HTTP / WS 且不注册 HTTPS 重定向；未设置或为 `false` 时使用 HTTPS / WSS |
| `mainConfig.tlsCertFile/tlsKeyFile` | HTTPS 模式生效 | HTTPS 证书与私钥路径，HTTP 模式不读取 |
| `mysqlConfig.user/password/host/port/databaseName` | 是 | 使用完整配置建立 TCP 连接 |
| `redisConfig.*` | 是 | |
| `authCodeConfig.accessKeyID/accessKeySecret` | 是 | |
| `authCodeConfig.signName/templateCode` | 否 | 代码中写死 |
| `logConfig.logPath` | 是 | 日志**文件**路径，不是目录 |
| `kafkaConfig.messageMode` | 是 | `channel`（默认）或 `kafka` |
| `kafkaConfig.hostPort/chatTopic/partition/timeout` | 是 | `partition` 实际被用作消息 key |
| `kafkaConfig.loginTopic/logoutTopic` | 否 | |
| `staticSrcConfig.*` | 是 | 头像与文件目录，**需手动创建**，代码不会自动创建 |

## 硬编码项清单

本地启动相关路径已经配置化，剩余环境相关项见下表（TD-03）。

| 位置 | 当前值 |
|---|---|
| `internal/config/config.go` `LoadConfig` | 默认相对路径；其他工作目录使用 `MARSCHAT_CONFIG` 指定 |
| `cmd/mars_chat_server/main.go` | 读取 `mainConfig.tlsCertFile/tlsKeyFile`，云部署必须同步设置 |
| `web/chat-server/src/store/index.js` | 默认本机 8000，HTTP 页面用 HTTP / WS，HTTPS 页面用 HTTPS / WSS；构建时可通过 `VUE_APP_BACKEND_URL`、`VUE_APP_WS_URL` 覆盖 |
| `web/chat-server/vue.config.js` | 默认 HTTP，监听 `127.0.0.1:8080`；仅当 `VUE_DEV_HTTPS=true` 时读取开发证书 |
| `web/chat-server/src/views/chat/contact/ContactChat.vue` | `ICE_CFG: {}`，未配置 STUN/TURN |
| `internal/service/sms/auth_code_service.go` | 短信签名与模板编码 |

## 本地开发（Windows / PowerShell）

### 容器与端口

[compose.yaml](../compose.yaml) 仅启动开发依赖，前后端仍在宿主机运行。使用独立网络和命名卷，不复用其他项目的容器及数据。

| 服务 | 已有镜像 | 宿主机地址 | 容器端口 |
|---|---|---|---|
| MySQL | `mysql:8.0` | `127.0.0.1:13307` | 3306 |
| Redis | `redis:7` | `127.0.0.1:16381` | 6379 |
| Kafka（单节点 KRaft） | `apache/kafka:4.3.1` | `127.0.0.1:19093` | 19093（外部）、9092（内部） |

所有服务设置 `pull_policy: never`，镜像缺失直接失败，不自动联网拉取。Kafka 不需要 ZooKeeper；`kafka-init` 在 broker 健康后幂等创建 `chat_message`（1 分区、1 副本），成功后退出是正常状态。

### 首次配置

在项目根目录执行；已有文件时应保留并核对，不要覆盖真实配置：

```powershell
Copy-Item .env.example .env
Copy-Item configs/config.toml configs/config_local.toml
New-Item -ItemType Directory -Force -Path static/avatars, static/files, pkg/ssl, cmd/mars_chat_server/logs
```

- 在 `.env` 中设置 `MYSQL_PASSWORD` 和 `MYSQL_ROOT_PASSWORD`；将同一个业务用户密码填入 `config_local.toml` 的 `mysqlConfig.password`。本次机器初始化已生成随机密码并写入这两个被忽略的文件。
- 模板已使用上表端口，数据库和用户均为 `marschat`。MySQL 初始化变量仅在空数据卷首次生效，修改 `.env` 不会修改已有数据库密码。
- 模板保留 `messageMode = "channel"`；本机 `config_local.toml` 使用 `messageMode = "kafka"`。可切回 `channel` 排查聊天问题；Kafka 模式仍有 TD-05、TD-07 等业务缺陷。
- Redis 实例应专用于本项目：当前后端关停仍会删除所选 Redis 库的全部键（TD-06）。

```powershell
docker compose config --quiet
docker compose up -d --pull never --wait --wait-timeout 120
docker compose ps -a
docker compose exec kafka /opt/kafka/bin/kafka-topics.sh --bootstrap-server kafka:9092 --describe --topic chat_message
```

### 无证书本地启动

模板与本机 `config_local.toml` 均设置 `mainConfig.httpEnabled = true`，后端监听 `127.0.0.1:8000`，不读取证书、不跳转 HTTPS。前端默认使用 HTTP，并自动匹配 HTTP / WS 后端地址。已有配置升级时只需在 `[mainConfig]` 中添加 `httpEnabled = true`，不要覆盖数据库凭据。

后端在一个终端运行：

```powershell
Set-Location cmd/mars_chat_server
go run .
```

前端在另一个终端从项目根目录运行：

```powershell
Set-Location web/chat-server
pnpm install --frozen-lockfile
pnpm serve
```

本项目使用 pnpm 11.22.0，`package.json` 已固定包管理器版本；不要再生成 Yarn/npm 锁文件。2026-11-09 已从旧 Yarn 锁文件迁移：16 个直接依赖版本保留，仅间接依赖 `@achrinza/node-ipc` 解析为支持 Node 24 的 9.2.10。锁文件冻结校验与 `pnpm peers check` 通过。开发者首次安装已完成依赖下载和链接，但被 `ERR_PNPM_IGNORED_BUILDS` 中止，随后补齐下述脚本策略；开发者现已确认成功登录。前端生产构建未验证，旧工具链的弃用警告仍存在。

`pnpm-workspace.yaml` 明确审核安装脚本：允许 `vue-demi` 按 Vue 版本生成兼容入口；禁用仅输出提示的 `core-js` 脚本及当前项目未使用的 `yorkie` Git hooks 安装脚本。其余依赖脚本仍需审核，不关闭 pnpm 的严格检查。如果之前安装因脚本未审批而中止，更新配置后执行：

```powershell
pnpm install --frozen-lockfile
pnpm rebuild vue-demi
pnpm serve
```

`rebuild` 用于确保之前被拦截的 `vue-demi` 脚本补执行；无需删除 node_modules、缓存或锁文件。

访问 `http://localhost:8080` 或 `http://127.0.0.1:8080`，后端为 `http://127.0.0.1:8000`（根路径没有页面，返回 404 正常）。本次按用户要求仅提供依赖安装和启动命令，由用户执行。若曾在前端 `.env.local` / `.env.development.local` 或终端中设置 HTTPS 地址，应删除旧覆盖或改为 HTTP / WS，并重启前端。

同机音视频可在两个独立浏览器会话中用不同账号测试，双方授权摄像头和麦克风。浏览器将 localhost / 回环地址视为可信本地环境；使用局域网 IP 访问其他设备时，不应依赖此例外，应改用可信 HTTPS。项目音视频连接尚未完成端到端验证。

手机访问 `http://<电脑局域网 IP>:8080` 时，无法使用 localhost 的安全上下文例外。文字聊天不依赖摄像头或麦克风，音视频发起、接听则需要浏览器信任的 HTTPS 页面和对应 HTTPS / WSS 后端。证书应包含访问用的局域网 IP 或域名，并由每台测试设备信任其签发 CA；仅在电脑上信任证书不足以让手机信任。2026-11-10 已修复媒体接口不可用时空流触发 `getTracks` 异常的问题，现在会提示原因并停止启动；此修复不会将 HTTP 页面变成安全上下文。

开发时可为测试手机号手动设置验证码，五分钟内在注册页直接填写，不点击“发送验证码”，从而避免调用真实短信服务：

```powershell
docker compose exec redis redis-cli SET auth_code_13800138000 123456 EX 300
```

首个管理员仍需手动修改数据库的 `user_info.is_admin`。

### 切回 HTTPS

将 `config_local.toml` 中 `httpEnabled` 设为 `false`，填写证书路径。安装 [mkcert](https://github.com/FiloSottile/mkcert#installation) 后可在根目录生成开发证书：

```powershell
mkcert -install
mkcert -cert-file pkg/ssl/local.pem -key-file pkg/ssl/local-key.pem localhost 127.0.0.1 ::1
```

在启动前端的终端设置 `$env:VUE_DEV_HTTPS = 'true'`，然后启动前端并访问 HTTPS 地址。前端会默认使用 HTTPS / WSS 后端；也可用 `VUE_APP_BACKEND_URL`、`VUE_APP_WS_URL` 显式指定。切回 HTTP 时移除 `VUE_DEV_HTTPS` 及旧的地址覆盖，并重启前后端。HTTP 模式仅用于本机开发，数据库端口与 Kafka 配置不变。

### 停止与恢复

`docker compose stop` 停止容器并保留数据，下次重新执行 `up` 即可恢复。`docker compose down` 移除本项目容器及网络，命名卷仍保留；不要加 `-v`，否则会删除开发数据库和消息数据。

### 验证记录

2026-11-09 提交前复核：开发者确认本地成功登录；`go vet ./cmd/... ./internal/... ./pkg/... ./api/...` 通过。`pnpm lint --no-fix` 被 pnpm 权限错误阻断，直接执行已安装的 `vue-cli-service lint --no-fix` 得到 51 个错误，均位于本次未修改的文件，包含未定义变量、未使用变量与组件规则问题；仅检查本次修改的 `src/store/index.js` 和 `vue.config.js` 时通过。未为此次提交修改这些既有问题；生产构建、聊天和音视频端到端仍未验证。

2026-11-09 无证书模式：`go vet ./cmd/... ./internal/... ./pkg/... ./api/...`、`go build ./cmd/mars_chat_server`、前端修改文件语法检查和 `git diff --check` 通过。通过独立 Node 检查确认 HTTP 配置不读取证书、显式 HTTPS 配置读取证书、前端地址协议随页面切换且允许环境变量覆盖。未安装前端依赖、未执行 yarn lint、未启动应用，页面与音视频由用户执行下述启动命令后验证。

2026-11-08 已验证：`docker compose config --quiet` 和 `up --pull never --wait` 通过，三个服务健康；MySQL 用户可查询 `marschat` 数据库，Redis 返回 PONG，`chat_message` 为 1 分区 / 1 副本，Kafka 外部监听器可返回协议能力；宿主机三个 TCP 端口均连通。`go vet ./cmd/... ./internal/... ./pkg/... ./api/...`、`go build ./cmd/mars_chat_server`、前端两个修改文件的 Node 语法检查及 `git diff --check` 通过。前端未安装 node_modules，因此未执行 yarn lint；尚未生成可信开发证书或启动前后端验证聊天。

## 云服务器部署（基线流程，Ubuntu 22.04）

基线流程把所有组件装在同一台服务器上：

1. 安装 MySQL、Redis；创建数据库，配置完整的 `mysqlConfig` TCP 参数；设置 `mainConfig.httpEnabled = false`，将 `mainConfig.tlsCertFile/tlsKeyFile` 设为云服务器证书路径。其他工作目录通过 `MARSCHAT_CONFIG` 指定配置文件。
2. 安装 Go、Node.js、pnpm（版本见前端 packageManager）。Vue CLI 由项目依赖提供，不需要全局安装。
3. 前端执行 `pnpm install --frozen-lockfile` 后运行 `pnpm build`，把 `dist/` 复制到 `/var/www/html`；Apache2 启用 `ssl` 模块，在 443 虚拟主机中配置证书，80 端口可重写到 HTTPS。
4. 用 openssl 自建根证书并签发服务器证书，输出到后端硬编码的两个路径。证书扩展需要包含服务器 IP，否则浏览器会拒绝：

   ```ini
   authorityKeyIdentifier=keyid,issuer
   basicConstraints=CA:FALSE
   keyUsage = digitalSignature, nonRepudiation, keyEncipherment, dataEncipherment
   subjectAltName = @alt_names
   [alt_names]
   IP.1 = <服务器公网 IP>
   ```

5. 安装 coturn 作为 TURN 服务（见下节）。
6. 构建后端并交给 systemd 托管。`WorkingDirectory` 要指向 `cmd/mars_chat_server`，因为静态目录等相对路径以它为基准：

   ```ini
   [Unit]
   Description=mars chat service
   After=network.target

   [Service]
   WorkingDirectory=/root/project/MarsChat/cmd/mars_chat_server
   ExecStart=/usr/local/bin/mars_chat_backend
   Restart=on-failure
   RestartSec=5

   [Install]
   WantedBy=multi-user.target
   ```

   ```bash
   cd cmd/mars_chat_server && go build -o mars_chat_backend main.go
   sudo cp mars_chat_backend /usr/local/bin/
   sudo systemctl daemon-reload && sudo systemctl enable --now mars_chat_backend
   ```

### 端口

| 端口 | 用途 |
|---|---|
| 443 | 前端 HTTPS |
| 80 | HTTP，重定向到 HTTPS |
| 8000 | 后端 HTTPS / WSS |
| 3478 | TURN |
| 22 | SSH |
| 3306、6379 | MySQL、Redis |

基线流程在云安全组中对公网开放了 3306 和 6379。这两个端口只需本机访问，**不应对公网开放**。

### TURN 与 ICE 配置

coturn 主要配置项：`listening-ip=0.0.0.0`、`external-ip=<公网 IP>`、`listening-port=3478`、`user=<用户名>:<密码>`，需要加密时再配置 `tls-certificate`、`tls-private-key`。

前端 `ContactChat.vue` 的 `ICE_CFG` 需要相应改为：

```js
ICE_CFG: {
  iceServers: [
    { urls: "stun:stun.l.google.com:19302" },
    { urls: "turn:<服务器 IP>:3478", username: "<用户名>", credential: "<密码>" },
  ],
},
```

只在局域网内使用时保持 `ICE_CFG` 为空即可。

### Kafka 模式

把 `messageMode` 改为 `kafka`，并预先创建 `chatTopic` 指定的 topic（代码不会自动创建）。kafka 模式存在已知缺陷（TD-05、TD-07），使用前请先阅读 [ADR-0002](architecture/adr/0002-message-mode-channel-kafka.md)。
