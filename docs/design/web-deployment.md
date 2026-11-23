# 设计：前端的容器化部署与 HTTPS

- 状态：已采纳（2026-11-22） | 日期：2026-11-22 | 关联：[前端设计](frontend.md) 3.10、第 10 节第 81～82 条，[gateway-multi-instance.md](gateway-multi-instance.md)，[turn-relay.md](turn-relay.md)，[前端计划](../plans/active/frontend-rewrite.md) 第 9 阶段

前端设计 3.10 约定：构建产物是静态文件，与网关同源，不开 CORS；具体方式在部署阶段单独设计。本文先以提议提交，开发者确认结果见第 8 节。

## 1. 背景与目标

现状（实施前）：

- 开发时由 Vite 提供页面，并把 `/v1`、`/ws` 代理到网关或宿主机上的服务（3.10）；
- 容器模式（`make cluster-up`）只有后端服务和 APISIX，没有前端，打开 `http://localhost:19080/` 得到的是网关的 `404 Route Not Found`；
- 网关只绑定 `127.0.0.1`，证书只包含 `localhost`、`127.0.0.1`、`::1`，其他设备无法访问。

目标：

- 容器模式下经网关同源提供前端，打开网关地址就能完整使用，包括通话；
- 复用开发时的 Compose 项目：MySQL、Redis、Kafka、Consul、数据卷和后端服务容器都不重复创建，只新增前端容器（和可选的 coturn）；
- 支持局域网内其他设备访问（开发者确认，第 8 节）；
- 给开源后的使用者留好 HTTPS 与域名的做法：本机、局域网、公网三种场景都有明确步骤，公网部署本身不在这次实施范围内。

开发流程不变，仍然用 `make web-dev`；部署后两种入口同时可用，连的是同一套后端和数据。

## 2. 需求与约束

- **同源**：页面、`/v1`、`/ws` 由同一个地址提供，前端用相对路径，不配置 CORS。
- **安全上下文**：浏览器只在 HTTPS 或 `localhost`（`127.0.0.1`、`[::1]`）下提供麦克风、摄像头和系统通知（前端设计第 81 条）。本机用 HTTP 也可以；局域网和公网必须用 HTTPS。
- **端口约定**：宿主机端口为默认端口 + 10000，默认只绑定 `127.0.0.1`（AGENTS.md）。网关和 coturn 是例外，绑定地址可以在 `.env` 中改；前端容器不映射宿主机端口，只由网关访问。
- **缓存**：带哈希的 `assets/*` 长期缓存；`index.html` 每次向服务器确认，发布新版本后刷新即生效。
- **单页应用路由**：`/chat/...`、`/settings/...` 刷新时返回 `index.html`；不存在的 `/v1/...` 必须是 404，不能回落成 `index.html` 的 200。
- **镜像构建方式与服务一致**：前端在宿主机 `pnpm build`，镜像里只复制产物，不装 Node。
- **不新增前端依赖**；新增 nginx 基础镜像（coturn 镜像见 turn-relay.md）。

## 3. 方案

### 3.1 前端容器

`deploy/web/Dockerfile` 与 `deploy/web/nginx.conf`，镜像 `marschat/web:dev`：

- 基础镜像 `nginxinc/nginx-unprivileged:1.30.6-alpine3.24-slim`，按标签加 digest 锁定。以非 root 用户运行、监听 8080，pid 与临时文件在 `/tmp`，容器以只读根文件系统运行，`/tmp` 用 tmpfs；
- 构建上下文是 `web/dist`，nginx 配置从 BuildKit 的命名上下文 `conf`（`deploy/web`）取，不用把产物复制到别处；
- `web/public/emoji-packs` 存在时随产物进入镜像（开发者确认已获授权）。

nginx 配置要点：

| 路径 | 处理 | 原因 |
|---|---|---|
| `/assets/*` | 文件不存在返回 404；`Cache-Control: public, max-age=31536000, immutable` | 带哈希的文件内容不变；缺文件时不能回落成 HTML，否则浏览器把 HTML 当脚本执行 |
| `^~ /v1/`、`= /ws` | 404 | 走到这里说明接口路径不存在 |
| 其他 | `try_files $uri /index.html`；`Cache-Control: no-cache` | 前端路由刷新；`index.html`、表情数据与图片每次向服务器确认 |

`Cache-Control` 用 `map` 按路径算出、在 server 级统一 `add_header`：location 里一旦写了 `add_header`，就不再继承 server 级的，`X-Content-Type-Options`、`Referrer-Policy`、`X-Frame-Options` 会丢。另外开启 gzip、关闭 `server_tokens`。

### 3.2 Compose 与网关

- `compose.yaml` 新增 `web` 服务，放在 `apps` profile，不映射宿主机端口，固定 IP `172.30.0.11`，带健康检查。nginx 不注册到 Consul，网关直接写节点地址；不用服务名，是因为 APISIX 会缓存 DNS 结果，容器重建换了 IP 后可能还连旧地址。
- `deploy/apisix/apisix.yaml` 新增上游 `web`（`172.30.0.11:8080`）与路由 `web`（`/`、`/*`，只允许 GET、HEAD）。接口和 WebSocket 路由的路径更具体，优先匹配，剩下的路径都交给前端。
- 前端路由单独限流：每个 IP 每秒 200 次、突发 400。不用接口的 `http-default`（每秒 50 次），因为打开表情面板时一次会请求上百张图片；经 Docker Desktop 端口转发时本机所有请求的来源 IP 相同，限流相当于全局生效。
- 证书匹配改为 `sni: "*"`：网关只有一张证书，不管客户端用什么域名访问都用它。换成自己域名的正式证书时只替换证书文件，不用改网关配置。用 IP 访问时浏览器不发 SNI，按 `fallback_sni` 落到同一张证书。
- 网关的宿主机端口改为可配置：`MARSCHAT_GATEWAY_HTTP_BIND`（默认 `127.0.0.1:19080`）、`MARSCHAT_GATEWAY_HTTPS_BIND`（默认 `127.0.0.1:19443`）。

### 3.3 构建命令

- `make web-image`：`pnpm build` 后构建 `marschat/web:dev`；
- `make images` 不包含前端，只构建 Go 服务时不要求先装前端依赖；
- `make cluster-up` 启动前检查前端镜像是否存在，缺失时提示先 `make web-image`；`make cluster-down` 一并删除前端容器。

### 3.4 前端改动

- **下载地址改为相对路径**：`api/files.ts` 的 `toRelativeUrl`（原 `toProxiedUrl`）在开发和生产环境都把 `/v1/files/{id}/content` 的地址改成相对路径，跟着页面走；OSS 的预签名地址原样保留。这样 `MARSCHAT_MEDIA_PUBLIC_BASE_URL` 只影响直接调用接口的客户端，不必和页面地址一致。
- WebSocket 地址已经按页面协议选择 `ws` 或 `wss`（`realtime/client.ts`），不用改。

## 4. HTTPS 与域名

### 4.1 三种场景

| 场景 | 访问地址 | 证书 | 是否需要域名 | 需要的设置 |
|---|---|---|---|---|
| 本机 | `http://localhost:19080`，或 `https://localhost:19443` | HTTP 即可（`localhost` 是安全上下文）；HTTPS 用 `make certs` 的自签证书 | 不需要 | 默认值 |
| 局域网 | `https://<本机局域网 IP>:19443` | 自签证书，额外包含局域网 IP；每台访问的设备信任一次 | 不需要；也可以用局域网 DNS 或 hosts 里的名字 | `MARSCHAT_GATEWAY_HTTPS_BIND`、`MARSCHAT_CERT_HOSTS`，见 4.2 |
| 公网 | `https://chat.example.com` | 公共 CA 签发的正式证书（例如 Let's Encrypt），需要自动续期 | 需要 | 见 4.3 |

结论：本机和局域网都不需要域名；面向他人的公网部署需要域名。公网只用 IP 访问时只能用自签证书，每个用户都要手动信任，浏览器还会反复警告，不适合给别人用。

### 4.2 局域网

1. `.env` 中设置（`192.168.1.10` 换成本机的局域网地址）：

   ```bash
   MARSCHAT_GATEWAY_HTTPS_BIND=192.168.1.10:19443
   MARSCHAT_CERT_HOSTS=192.168.1.10
   MARSCHAT_MEDIA_PUBLIC_BASE_URL=https://192.168.1.10:19443
   ```

   HTTP 端口保持默认的 `127.0.0.1`：局域网内用 HTTP 打开页面拿不到麦克风和摄像头，不对外开放。
2. 删除 `deploy/apisix/certs/` 下已有的 `tls.crt`、`tls.key`，`make certs` 重新生成（已有证书时 `make certs` 不覆盖，只提示缺少哪些名字），再 `make cluster-up`。
3. 每台访问的设备信任一次 `deploy/apisix/certs/tls.crt`（只拷贝证书，不要拷贝私钥）：Windows 导入「受信任的根证书颁发机构」，macOS 钥匙串设为始终信任，iOS 安装描述文件后在「证书信任设置」中开启，Android 在「加密与凭据」中安装 CA 证书。不信任时浏览器会警告，点「继续访问」后能否使用麦克风因浏览器而异，以信任证书为准。
4. Windows 防火墙可能拦截其他设备的入站连接，需要时由使用者自行放行这个端口。

已经有本地 CA 工具（例如 mkcert）的使用者，也可以用它签发证书放到 `deploy/apisix/certs/`，网关不关心证书来源。

### 4.3 公网（给开源使用者的做法，这次不实施）

1. 域名解析到服务器；`.env` 中 `MARSCHAT_GATEWAY_HTTPS_BIND=0.0.0.0:443`，`MARSCHAT_MEDIA_PUBLIC_BASE_URL=https://chat.example.com`；HTTP 端口保持只绑定 `127.0.0.1`，或者绑定 80 后在网关加 `redirect` 插件跳转到 HTTPS。
2. 用 ACME 客户端申请证书，推荐 DNS 验证（acme.sh 或 certbot 的 DNS 插件）：不用占 80 端口，也不用给网关加验证路由。把证书链和私钥写成 `deploy/apisix/certs/tls.crt`、`tls.key`。
3. 网关在启动时读取证书，续期后执行 `docker compose --profile apps restart apisix`；可以写进 ACME 客户端的续期钩子。
4. TURN 与网关用同一个域名（例如 `turn:chat.example.com:3478`），coturn 用主机网络和更大的中继端口段，见 [turn-relay.md](turn-relay.md) 第 6 节。
5. 公网部署还要考虑的事项另写计划：HSTS、对象存储（OSS）、只开放 443 与 TURN 端口、备份与监控。

| 问题 | 采用 | 不采用的方案与原因 |
|---|---|---|
| 证书由谁终止 | APISIX（已经在用，`sni: "*"` 后换证书只替换文件） | 在前面再加一层 Caddy 自动申请证书：多一个组件、多一跳代理，转发的客户端地址还要重新配置信任 |
| 公网证书怎么拿 | 使用者自选 ACME 客户端，网关只读文件 | 在网关里自动申请：APISIX 没有内置 ACME，第三方插件维护情况不确定 |

## 5. 接口、配置与数据变化

- 新增 `deploy/web/`、`compose.yaml` 的 `web` 服务、`apisix.yaml` 的 `web` 上游和路由、Makefile 的 `web-image`；
- 网关端口、证书名单、下载地址前缀可配置：`MARSCHAT_GATEWAY_HTTP_BIND`、`MARSCHAT_GATEWAY_HTTPS_BIND`、`MARSCHAT_CERT_HOSTS`、`MARSCHAT_MEDIA_PUBLIC_BASE_URL`，模板见 `.env.example`；`tools/devcert` 新增 `-hosts`；
- 前端 `api/files.ts`：生产环境也改写下载地址；
- 不涉及接口、数据库和 Redis。

## 6. 候选方案与权衡

| 问题 | 采用 | 备选与不采用的原因 |
|---|---|---|
| 谁提供静态文件 | 单独的 nginx 容器，网关转发 | 挂进 APISIX，用它的 nginx 配置片段提供：升级 APISIX 时容易失效，前端发布和网关绑在一起；由某个 Go 服务提供：职责混杂 |
| 镜像里怎么得到产物 | 宿主机 `pnpm build` 后打包 | 多阶段构建在容器里装依赖：与服务镜像的做法不一致，Docker Desktop 虚拟机内存有限 |
| 网关怎么找到 nginx | 固定 IP | 服务名：APISIX 缓存 DNS；注册到 Consul：nginx 需要额外的注册方式 |
| 局域网访问怎么开 | 绑定地址用环境变量配置，默认仍是 `127.0.0.1` | 直接绑定 `0.0.0.0`：所有使用者的网关都暴露在局域网，测试账号也能被别人登录 |

## 7. 风险与验证

| 风险 | 处理 |
|---|---|
| 局域网访问时网关暴露给同一网络的所有设备 | 默认不开启；开启时只绑定 HTTPS 端口，测试账号使用弱密码时注意 |
| 自签证书要在每台设备上信任 | 4.2 第 3 步；或者用自己的本地 CA |
| 发布新版本时有页面开着 | `index.html` 不缓存，刷新即得到新版本；没刷新的页面切换到已删除的旧文件时显示「页面加载失败」和刷新按钮（第 82 条） |
| 单文件挂载的配置被编辑器整体替换后，容器看不到新内容 | `apisix.yaml` 是单文件挂载，编辑器保存时如果写新文件再改名，容器里仍是旧文件（Docker Desktop 上看到的内容不完整，没有 `#END`，APISIX 保留旧配置）。改完后检查容器里文件末尾的 `#END`，不对就重建网关容器，见 deployment.md |

验证结果见前端计划第 9 阶段。

## 8. 确认结果（2026-11-22）

开发者确认：

1. **方案**：单独的 nginx 容器、固定 IP、网关 `/` 路由兜底。复用开发时的 Compose 项目与容器，只新增前端容器。
2. **平台表情**：进入镜像。开发者确认表情已获授权。
3. **跨设备访问**：支持局域网访问（第 4.2 节）；HTTPS 与域名的做法要写清楚，供开源后的使用者参考（第 4 节）。
4. **公网部署**：这次不实施，第 4.3 节给出做法；coturn 按 [turn-relay.md](turn-relay.md) 一起实施。
