# 部署与运行

> 本文根据代码和基线部署流程整理，**2026-11-07 未实际执行验证**。照此操作发现出入时，请以实际结果修正本文。

## 运行依赖

| 组件 | 是否必需 | 说明 |
|---|---|---|
| Go | 构建必需 | go.mod 要求 1.27.3 |
| MySQL | 必需 | 启动时自动建表，但数据库需要预先创建，库名与 `mainConfig.appName` 相同 |
| Redis | 必需 | 缓存与短信验证码 |
| TLS 证书 | 必需 | 后端只以 HTTPS/WSS 方式监听（`RunTLS`） |
| 阿里云短信 | 注册与验证码登录必需 | 需要 AccessKey；签名和模板目前写死在代码中（TD-13） |
| Node.js + yarn | 前端构建必需 | 基线流程使用 Node 16 与 Vue CLI；其他版本未验证 |
| Web 服务器 | 部署前端必需 | 基线流程使用 Apache2 托管 `dist/` 并开启 SSL |
| Kafka | 可选 | 仅 `messageMode = "kafka"` 时需要；topic 需手动预建 |
| TURN 服务 | 可选 | 公网音视频通话需要，基线流程使用 coturn |

## 配置文件

模板为 [configs/config.toml](../configs/config.toml)。实际读取的是 `config_local.toml`（被 `.gitignore` 忽略，可放真实密钥），路径写死在代码里，见下文“硬编码项清单”。

| 配置项 | 是否生效 | 说明 |
|---|---|---|
| `mainConfig.appName` | 是 | **同时被用作 MySQL 数据库名** |
| `mainConfig.host` / `port` | 是 | 监听地址，也用作 HTTPS 重定向的目标主机 |
| `mysqlConfig.user` | 是 | 通过 Unix Socket 连接，不使用密码 |
| `mysqlConfig.password/host/port/databaseName` | 否 | 连接串中未使用（TD-03） |
| `redisConfig.*` | 是 | |
| `authCodeConfig.accessKeyID/accessKeySecret` | 是 | |
| `authCodeConfig.signName/templateCode` | 否 | 代码中写死 |
| `logConfig.logPath` | 是 | 日志**文件**路径，不是目录 |
| `kafkaConfig.messageMode` | 是 | `channel`（默认）或 `kafka` |
| `kafkaConfig.hostPort/chatTopic/partition/timeout` | 是 | `partition` 实际被用作消息 key |
| `kafkaConfig.loginTopic/logoutTopic` | 否 | |
| `staticSrcConfig.*` | 是 | 头像与文件目录，**需手动创建**，代码不会自动创建 |

## 硬编码项清单

换环境运行前需要修改以下位置（TD-03）。多数位置的代码注释里保留了 Windows 本地开发用的写法。

| 位置 | 当前值 |
|---|---|
| `internal/config/config.go` `LoadConfig` | 配置文件 `/root/project/MarsChat/configs/config_local.toml` |
| `internal/dao/gorm.go` | DSN `<user>@unix(/var/run/mysqld/mysqld.sock)/<appName>`；注释中有 TCP 连接写法 |
| `cmd/mars_chat_server/main.go` | 证书 `/etc/ssl/certs/server.crt`、私钥 `/etc/ssl/private/server.key`；注释中有 `pkg/ssl/127.0.0.1+2.pem` 写法 |
| `web/chat-server/src/store/index.js` | `backendUrl`、`wsUrl` 为某台云服务器的公网 IP |
| `web/chat-server/vue.config.js` | 开发服务器读取 `/etc/ssl/...` 证书并监听 443；注释中有本地证书与 8080 端口的写法 |
| `web/chat-server/src/views/chat/contact/ContactChat.vue` | `ICE_CFG: {}`，未配置 STUN/TURN |
| `internal/service/sms/auth_code_service.go` | 短信签名与模板编码 |

## 本地开发（未验证）

1. 准备 MySQL 与 Redis，创建与 `appName` 同名的数据库。
2. 复制 `configs/config.toml` 为 `config_local.toml` 并填写；按上表把配置路径、DSN、证书路径改为本地值。
3. 用 mkcert 等工具为 `127.0.0.1` 生成证书放到 `pkg/ssl/`（已被 `.gitignore` 忽略），浏览器需信任该证书，并先访问一次 `https://127.0.0.1:8000` 接受证书。
4. 手动创建 `staticSrcConfig` 中的两个目录，然后：

   ```bash
   cd cmd/mars_chat_server && go run main.go
   ```

5. 前端：修改 `store/index.js` 的后端地址和 `vue.config.js` 的证书配置后：

   ```bash
   cd web/chat-server && yarn install && yarn serve
   ```

6. 注册需要短信验证码。当前实现会先把验证码写入 Redis 并打印到后端标准输出，再调用短信接口，因此即使短信发送失败，开发环境也能从输出中取得验证码（这一行为本身是待修复的问题，TD-13）。
7. 首个管理员需要手动设置：`UPDATE user_info SET is_admin = 1 WHERE telephone = '<手机号>';`

## 云服务器部署（基线流程，Ubuntu 22.04）

基线流程把所有组件装在同一台服务器上：

1. 安装 MySQL、Redis；创建数据库。
2. 安装 Go、Node.js、yarn、Vue CLI。
3. 前端 `yarn build`，把 `dist/` 复制到 `/var/www/html`；Apache2 启用 `ssl` 模块，在 443 虚拟主机中配置证书，80 端口可重写到 HTTPS。
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
