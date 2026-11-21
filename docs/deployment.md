# 部署与运行

> 本文描述**新后端**的运行方式，随重构步骤更新，最近一次更新于步骤 8（2026-11-21）。服务有两种运行方式：宿主机运行（日常开发）和容器模式（多实例实验，经网关访问）。旧单体的部署流程见提交 `6624cea` 中的本文。

## 运行依赖

| 工具 | 版本 | 用途 |
|---|---|---|
| Go | 1.27.3（`go.mod`） | 构建与运行服务 |
| Docker Desktop | 本机 29.9.2，Compose v5.6.1 | 运行依赖组件 |
| buf | 1.71.0 | `make lint`、`make conf`、`make api`；protoc 插件由 `go run` 按固定版本下载，不需要单独安装 |
| make + Git Bash | GNU make 3.81 及以上 | 开发命令；Makefile 使用 `sh`，Windows 上需要 Git Bash |

## 依赖组件

[compose.yaml](../compose.yaml) 定义依赖组件和容器模式的应用服务（profile `apps`，见“容器模式”），项目名 `marschat`，数据保存在命名卷中。宿主机端口为容器默认端口 + 10000，只绑定 `127.0.0.1`。

网络使用固定子网 `172.30.0.0/24`：前半段留给固定地址（步骤 5 的网关），容器的动态地址只从 `172.30.0.128/25` 分配。本机其他项目的网络占用 172.17～172.26 网段。修改网络配置后要先 `make down` 再 `make up` 重建容器，数据卷不受影响。

| 组件 | 镜像 | 宿主机地址 | 说明 |
|---|---|---|---|
| MySQL | `mysql:9.7.4` | `127.0.0.1:13306` | 首次初始化时为每个服务建库建账号，见 [data-model.md](architecture/data-model.md#1-数据库与账号)。启动参数：慢查询日志（超过 200 毫秒，见“慢查询日志”）、最大连接数 300（见“容器模式”的连接预算）；修改参数需要重建容器（`docker compose up -d --wait mysql`），数据卷保留 |
| Redis | `redis:8.8.4` | `127.0.0.1:16379` | 开启 AOF 持久化和密码；加载镜像自带的 RedisBloom 模块（用户、群 ID 布隆过滤器）。**最低 8.0**：在线路由用 `HSETEX` 设置 Hash 字段级 TTL |
| Kafka | `apache/kafka:4.4.0` | `127.0.0.1:19092` | 单节点 KRaft；容器网络内地址为 `kafka:29092`；关闭自动建 topic，topic 由 `kafka-init` 显式创建；堆内存限制为 512 MiB（镜像默认 1 GiB，Docker Desktop 虚拟机只有 3.8 GiB） |
| topic 初始化 | `apache/kafka:4.4.0` | — | `kafka-init` 一次性任务，带 profile，由 `make up` 在依赖组件健康后执行：`kafka-topics.sh --create --if-not-exists` 创建 `marschat.account.events`（3 分区）、`marschat.social.events`（6）、`marschat.message.events`（12）、重试队列 `marschat.social-cache.retry`（3）、死信队列 `marschat.dlq`（3，保留 14 天），以及 e2e 专用的 `marschat.e2e.events`、`marschat.e2e.retry`（各 3），副本数 1；重复执行不会报错，已有环境新增 topic 时执行一次 `docker compose run --rm kafka-init` |
| Consul | `hashicorp/consul:2.1.0` | `127.0.0.1:18500` | 单节点 server，带 Web UI |
| 迁移任务 | `migrate/migrate:v4.20.1` | — | `migrate-<服务>` 一次性任务，带 profile，`make up` 不启动，见“数据库迁移” |

监控组件（profile `monitoring`）见“监控”，压测工具 k6（profile `loadtest`）见“压测”；全部应用端口见 [重构计划的端口表](plans/active/backend-refactor.md#端口)。

## 首次配置

在项目根目录（Git Bash）执行：

```bash
cp .env.example .env
make keys    # 生成 JWT 密钥对和 media 下载地址签名密钥并追加到 .env，已有时不覆盖，不打印密钥
```

把 `.env` 中的 `change-me` 换成随机密码，只用字母和数字，避免在 SQL 和 DSN 中转义。`.env` 被 Git 忽略，同时供 Compose 和本机运行的服务使用。

| 变量 | 用途 |
|---|---|
| `MARSCHAT_MYSQL_ROOT_PASSWORD` | MySQL root 密码 |
| `MARSCHAT_ACCOUNT_DB_PASSWORD`、`MARSCHAT_SOCIAL_DB_PASSWORD`、`MARSCHAT_MESSAGE_DB_PASSWORD`、`MARSCHAT_MEDIA_DB_PASSWORD` | 各服务数据库账号的密码 |
| `MARSCHAT_REDIS_PASSWORD` | Redis 密码 |
| `MARSCHAT_JWT_PRIVATE_KEY` | Ed25519 私钥种子（32 字节）的 base64，只有 account 使用。由 `make keys` 生成，更换后已签发的令牌全部失效 |
| `MARSCHAT_JWT_PUBLIC_KEY` | 对应的公钥 base64，所有服务用来校验访问令牌；account 启动时检查它与私钥匹配 |
| `MARSCHAT_MEDIA_URL_SECRET` | media 本地存储下载地址的 HMAC 签名密钥（32 字节随机数的 base64），由 `make keys` 生成。使用 OSS 时不需要，见“文件存储” |
| `MARSCHAT_GRAFANA_ADMIN_PASSWORD` | Grafana 管理员 `admin` 的密码（24 字节随机数的 base64url），由 `make keys` 生成，只用于修改面板；查看面板不需要登录，见“监控” |

MySQL 的建库脚本和 root 密码只在数据卷**首次初始化**时生效，之后修改 `.env` 不会改变已有账号的密码。需要重置时见“停止与重置”。

**Docker Desktop 文件共享**：本机 Docker Desktop 使用 Hyper-V 后端，挂载宿主机目录前需要在 Settings → Resources → File sharing 中加入该目录，否则 Docker Desktop 会弹窗询问，确认前 `docker compose up` 会一直卡住。建议直接加入项目根目录，APISIX、Prometheus、Grafana、Jaeger 的配置，media 的 `data/media` 目录和压测的 `test/load`、`tmp/load` 都会挂载。WSL 2 后端不需要这一步。排查过程见 [lessons/docker-desktop-bind-mount-hang.md](lessons/docker-desktop-bind-mount-hang.md)。

## 启动依赖组件

```bash
make up      # docker compose up -d --wait 等待全部健康检查通过，再执行 kafka-init 创建 topic
make ps
```

Consul UI：<http://127.0.0.1:18500>。

## 数据库迁移

迁移文件在 `app/<服务>/migrations/`（golang-migrate 格式，`<版本>_<名称>.up.sql` / `.down.sql`），由 compose 中的一次性任务执行，服务启动时不自动迁移：多实例同时启动会竞争，生产上也不应由业务进程改表结构。

```bash
make migrate-account                    # 执行全部未执行的迁移（up）
make migrate-social
make migrate-message
make migrate-media
make migrate-account ARGS="down 1"      # 回滚一步
make migrate-account ARGS="version"     # 查看当前版本
```

迁移记录在各库的 `schema_migrations` 表。首次运行会拉取 `migrate/migrate` 镜像；迁移目录通过 bind mount 挂载，Docker Desktop 需要共享项目根目录（见“首次配置”）。

**升级顺序**：先迁移，再换新版本的服务。例如步骤 8 给 account、social、message 的 `outbox_events` 加了 `trace_parent` 列，新版本的服务写入这一列，没迁移的库会让写事件的事务全部失败；反过来，迁移后旧版本的服务照常运行（新列有默认值）。

## 运行服务

每个服务一个终端：

```bash
make run-account    # HTTP 127.0.0.1:18010，gRPC 127.0.0.1:19010
make run-social     # HTTP 127.0.0.1:18020，gRPC 127.0.0.1:19020
make run-message    # HTTP 127.0.0.1:18030，gRPC 127.0.0.1:19030
make run-access     # WebSocket ws://127.0.0.1:18000/ws，内部 gRPC 127.0.0.1:19100
make run-delivery   # gRPC 127.0.0.1:19040（只用于注册和健康检查）
make run-media      # HTTP 127.0.0.1:18050，gRPC 127.0.0.1:19050；文件默认存放在仓库下的 data/media/
```

`make run-<服务>` 会加载 `.env`，再执行 `go run ./app/<服务>/cmd/<服务> -conf app/<服务>/configs`。服务启动时连接本服务的 MySQL（access、delivery 没有）、Redis 并注册到 Consul，任一依赖不可用会直接退出。首次启动前要先执行对应的 `make migrate-<服务>`。

服务之间经 Consul 发现，启动顺序不限。被调用的服务或 Kafka 不可用时，调用方照常启动并降级运行：

| 不可用 | 影响 |
|---|---|
| account | social 申请好友返回 `503 DEPENDENCY_UNAVAILABLE`；social、message 的列表不带昵称和头像，记 WARN |
| social | 发消息返回 `503 DEPENDENCY_UNAVAILABLE`（无法校验发言权限）；发起呼叫返回 `503 UPSTREAM_UNAVAILABLE`，进行中的通话不受影响；会话列表不带群资料；群消息不推送 |
| media | 无法上传文件和换取下载地址；发送图片、文件消息返回 `503 DEPENDENCY_UNAVAILABLE`（无法核实文件），文本消息不受影响；使用 OSS 时已签发的下载地址在有效期内仍可下载 |
| message | WebSocket 连接照常建立和接收推送，发消息返回 `503 UPSTREAM_UNAVAILABLE` |
| access | 客户端无法建立长连接，HTTP 接口不受影响；单个 access 实例停机或崩溃会结束其上连接参与的通话（见 [data-flow.md](architecture/data-flow.md#94-超时与清理)） |
| delivery 或 Kafka | 消息照常写入、可以补拉，但没有实时推送和事件踢线（心跳兜底，最迟 30 秒断开被吊销的连接）；事件积压在 Outbox 表中，恢复后补发 |

在终端按 Ctrl+C 停止服务。按 Kratos 的停机流程，服务会先从 Consul 注销再退出（宿主机上未在 Windows 终端中实测；容器模式下 `docker stop` 发送 SIGTERM，已确认会先注销）。进程被强制结束时来不及注销，Consul 中的实例在 20 秒内变为 critical、不再被发现，10 分钟后自动删除。

检查服务状态：

```bash
curl -s http://127.0.0.1:18010/healthz   # 存活探针
curl -s http://127.0.0.1:18010/readyz    # 就绪探针，检查 MySQL 和 Redis
make e2e                                 # 全部服务的端到端测试，见“开发命令”
```

调用 account 的 HTTP 接口时要带 `Content-Type: application/json`，否则请求体按表单解析。开发环境的邮箱验证码固定为 `000000`（只写日志的 `log` 发送方，见“验证码邮件”），例如：

```bash
curl -s -X POST http://127.0.0.1:18010/v1/auth/email-codes \
  -H 'Content-Type: application/json' \
  -d '{"email":"alice@example.test","purpose":"CODE_PURPOSE_REGISTER"}'
```

接口列表见 [api/openapi.yaml](../api/openapi.yaml)。Git Bash 中 curl 的命令行参数按系统代码页传递，请求体里直接写中文会被服务端判为非法 UTF-8，测试中文时把请求体放进 UTF-8 文件再用 `-d @文件名`。

### 设置管理员

系统不提供设置管理员的接口。用户注册后执行：

```bash
make grant-admin EMAIL=alice@example.test
```

脚本 [scripts/grant-admin.sh](../scripts/grant-admin.sh) 把邮箱转为小写、校验字符后，在 MySQL 容器中把该用户的 `role` 改为管理员并删除资料缓存。新角色写在访问令牌里，该用户下次刷新令牌或重新登录后生效。

## 容器模式

用于多实例与故障实验（重构步骤 5）。服务镜像只含交叉编译的静态二进制，配置目录以只读方式挂载，地址类配置由 compose 中的 `MARSCHAT_` 环境变量覆盖。

```bash
make up                 # 依赖组件（已运行可跳过）；首次还要执行各服务的 make migrate-<服务>
make images             # 宿主机交叉编译，构建 marschat/<服务>:dev
make cluster-up         # docker compose --profile apps up -d --wait
make cluster-test       # 集群测试与故障实验（test/cluster，只经网关访问）
make cluster-down       # 只删除应用服务和网关的容器，依赖组件保持运行
```

`make cluster-up` 会先执行 `make certs`，为网关生成自签证书（已有时跳过）。

| 服务 | 实例数 | 容器内监听 |
|---|---|---|
| account | 1 | HTTP 18010、gRPC 19010 |
| social | 1 | HTTP 18020、gRPC 19020 |
| message | 2 | HTTP 18030、gRPC 19030 |
| delivery | 2 | HTTP 18040（探针与 `/metrics`）、gRPC 19040 |
| access | 2 | HTTP 18000（WebSocket）、gRPC 19100 |
| media | 1 | HTTP 18050、gRPC 19050；挂载仓库的 `data/media`（本地存储） |
| apisix（网关） | 1 | 9080、9443，发布到宿主机 `127.0.0.1:19080`、`127.0.0.1:19443`；固定 IP `172.30.0.10` |

- **与宿主机运行互斥**：两种模式共用 Consul 且服务名相同，宿主机实例注册 `127.0.0.1`，容器访问不到；容器实例注册容器 IP，宿主机访问不到。切换前先停掉另一种。宿主机进程被强制结束时，它在 Consul 中的实例要 20 秒后才变为 critical，这段时间内不要启动容器模式。
- **不发布宿主机端口**：应用容器只在 Compose 网络内可访问，客户端入口是网关（见下节）。依赖组件的宿主机端口照常可用。
- **镜像**：运行镜像为 `gcr.io/distroless/static-debian13:nonroot`，按 digest 锁定（见 [Dockerfile](../deploy/docker/Dockerfile)），没有 shell，不能 `docker exec` 进去排查；需要在网络内发请求时，临时起一个带 curl 的容器加入 `marschat_default` 网络。二进制默认按 amd64 编译，ARM 机器用 `make images IMAGE_ARCH=arm64`。
- **注册与健康检查**：容器内开启 Consul 的 TCP 检查，间隔 3 秒（`MARSCHAT_CONSUL_TCP_CHECK_INTERVAL`），TTL 心跳超时 6 秒，实例被杀后几秒内从发现结果中消失。HTTP 端点排在第一个，Consul 中服务的 `Address:Port` 是 HTTP 端口，网关按它转发。
- **资源**：每个应用容器限制 256 MiB 内存；media 解码图片占内存较多，限制为 384 MiB，并设置 `GOMEMLIMIT=320MiB`，让 GC 在接近上限前回收。2026-11-21 实测：media 空闲约 18 MiB，并发上传 1590 万像素的 16 位 PNG 时进程峰值 335 MiB（设计 [media-service.md](design/media-service.md) 第 10 节）；包括 media 在内，MarsChat 全部容器空闲时合计约 1.2 GiB。2026-11-19 实测：应用容器各 15～56 MiB，Kafka 512 MiB、MySQL 452 MiB，MarsChat 合计约 1.2 GiB。
- **数据库连接预算**：所有服务共用一个 MySQL，各实例连接池上限（`mysql.max_open_conns`）之和为 230：account 50、social 50、message 50 × 2、media 30。MySQL 最大连接数设为 300，留出余量给迁移和排查。增加实例或调大连接池时要同步核对：超过上限时 MySQL 直接拒绝连接（`Error 1040: Too many connections`），请求返回 500。2026-11-21 压测 1000 条连接时 MySQL 峰值用到 158 条，默认的 151 已经不够；把连接池缩小到合计 120 反而让 message 排队超时，触发 access 的熔断，见[重构计划的压测结果](plans/active/backend-refactor.md#压测结果)。
- **日志**：JSON 格式，`docker compose logs -f access` 查看；同一服务的多个实例日志会合并输出，容器名区分实例。日志带 `trace_id`、`span_id` 时可以到 Jaeger 中查看整条链路，见“监控”。
- **配置热更新**：配置目录挂载自宿主机，修改 `policy` 段后容器内同样自动生效（2026-11-19 验证，0.5 秒内出现 `config reloaded`）。
- **故障实验**：`make cluster-test` 包含 7 项故障实验，全部运行约 4 分钟。实验会对 MarsChat 自己的容器执行 `docker kill`、`restart`、`pause`、`stop`（access、delivery、message、Kafka、Redis），结束时（包括失败）恢复并等待其在 Consul 中恢复 passing 或健康检查通过。实验期间不要同时使用这套环境。只跑集群测试时加 `-short`：`set -a && . ./.env && set +a && go test -tags cluster -short -v ./test/cluster/...`。实验结果见[重构计划](plans/active/backend-refactor.md#故障实验结果)。

### 网关

APISIX `3.20.0`（镜像 `apache/apisix:3.20.0-debian`），Standalone 模式：不依赖 etcd，没有 Admin API，配置全部来自 [deploy/apisix/](../deploy/apisix/)。

| 文件 | 内容 | 生效方式 |
|---|---|---|
| `config.yaml` | 监听端口、Consul 发现、启用的插件（`limit-req`、`limit-count`、`limit-conn`、`client-control`、`real-ip`）、worker 数（2） | 重启容器 |
| `apisix.yaml` | 路由、上游、证书、限流、全局规则，文件以 `#END` 结尾 | **热更新**：保存后约 1 秒内生效，日志出现 `apisix.yaml reloaded` |
| `certs/` | `make certs` 生成的自签证书（`localhost`、`127.0.0.1`），被 git 忽略 | 重启容器 |

路由（未列出的路径由网关返回 404，`/healthz`、`/readyz` 不对外）：

| 路径 | 上游（Consul 服务名） |
|---|---|
| `/v1/auth/*`、`/v1/users/*`、`/v1/sessions`、`/v1/sessions/*`、`/v1/admin/*` | `marschat-account` |
| `/v1/friend-requests[/*]`、`/v1/friends[/*]`、`/v1/blocks[/*]`、`/v1/groups[/*]`、`/v1/group-join-requests/*` | `marschat-social` |
| `/v1/conversations[/*]` | `marschat-message` |
| `/v1/files`、`/v1/files/*` | `marschat-media`（请求体上限 101 MiB，超过时网关直接返回 413；发送、读取超时 300 秒） |
| `/ws`（WebSocket） | `marschat-access`（`least_conn`，读超时 120 秒） |

- **服务发现**：按服务名取 Consul 中检查通过的实例，节点为服务的 `Address:Port`（HTTP 端口）。阻塞查询，实例变化约 1 秒内同步。HTTP 上游连接失败时换节点重试一次。2026-11-19 实测：杀掉一个 message 实例后连续请求没有失败，只有约 5 秒内的两个请求因 2 秒连接超时后重试而变慢；实例重启后约 2.5 秒重新接到流量。
- **入口限流**（按客户端 IP，超限返回 429）：全部 HTTP 接口每秒 50 次、突发 100；`/v1/auth/*` 另加每分钟 60 次；`/ws` 每个 IP 最多 50 条并发连接、每秒 10 次握手。计数在网关进程内。
- **客户端 IP**：经 Docker Desktop 端口转发访问时，本机所有请求的来源地址都是 compose 网络的网关 `172.30.0.128`，限流对本机流量相当于全局限流；account 看到的客户端 IP 也是它，集群测试清理的是这个 IP 的限流计数。
- **压测来源**：全局规则 `real-ip` 只对 k6 容器的固定 IP `172.30.0.20` 信任 `X-Forwarded-For`，把其中最后一个地址作为 `remote_addr`；k6 给每个虚拟客户端分配 `198.18.0.0/15`（RFC 2544 基准测试网段）中的不同地址，网关和 account 的按 IP 限流按虚拟客户端生效。其他来源带的 `X-Forwarded-For` 不改变 `remote_addr`。这个地址在固定地址段中，不会被动态分配给其他容器。
- **TLS**：`https://localhost:19443`、`wss://localhost:19443/ws`；用 IP 访问时按 `localhost` 匹配证书。证书经启动命令读入环境变量，由 `apisix.yaml` 用 `$env://` 引用。curl 需要指定证书：

```bash
curl --cacert deploy/apisix/certs/tls.crt https://localhost:19443/v1/users/me
```

- **新增插件**：`config.yaml` 的插件列表要重启网关才生效（`docker compose --profile apps restart apisix`），而 `apisix.yaml` 是热更新的。在重启前就引用新插件的路由会被单独拒绝（错误日志 `unknown plugin`），其他路由不受影响；重启后正常。media 路由用到的 `client-control`（请求体上限）就是这样加入的。
- **日志**：`docker compose logs -f apisix`，访问日志和错误日志都输出到容器的标准输出。

## 监控

监控组件在 profile `monitoring` 中，只能抓取容器模式的服务（宿主机运行的服务注册的是 `127.0.0.1`，容器访问不到；需要时直接 `curl http://127.0.0.1:<HTTP 端口>/metrics`）。设计见 [observability.md](design/observability.md)。

```bash
make cluster-up        # 先启动容器模式
make monitoring-up     # 检查 .env 中有 Grafana 管理员密码，再启动并等待健康检查通过
make monitoring-down   # 删除监控组件的容器，Prometheus 数据卷保留
```

| 组件 | 镜像 | 宿主机地址 | 内存限制 | 说明 |
|---|---|---|---|---|
| Prometheus | `prom/prometheus:v3.16.0` | <http://127.0.0.1:19090> | 256 MiB | 经 Consul 发现 `marschat-*` 服务的全部实例，每 5 秒抓取 HTTP 端口的 `/metrics`；数据保留 3 天或 1 GB，卷 `prometheus-data`；告警规则每 5 秒计算一次 |
| Alertmanager | `prom/alertmanager:v0.35.0` | <http://127.0.0.1:19093> | 64 MiB | 接收告警，按告警名和服务分组，不发外部通知 |
| Grafana | `grafana/grafana:13.3.1` | <http://127.0.0.1:13000> | 384 MiB（`GOMEMLIMIT=320MiB`） | 匿名只读；预置数据源 Prometheus、Alertmanager 和面板“MarsChat 总览”；管理员 `admin`，密码为 `.env` 中的 `MARSCHAT_GRAFANA_ADMIN_PASSWORD` |
| Jaeger | `jaegertracing/jaeger:2.23.0` | UI <http://127.0.0.1:26686>；OTLP `127.0.0.1:14317`（gRPC）、`127.0.0.1:14318`（HTTP） | 256 MiB（`GOMEMLIMIT=200MiB`） | 内存存储，最多保留 20000 条 Trace，重启后丢失；容器网络内的服务发往 `jaeger:4317` |

- **配置文件**：`deploy/prometheus/`（抓取配置、告警规则 `rules.yml` 及其单元测试 `rules_test.yml`）、`deploy/alertmanager/`、`deploy/grafana/`（数据源、面板）、`deploy/jaeger/`。Prometheus 开启了 `--web.enable-lifecycle`，改规则后 `curl -X POST http://127.0.0.1:19090/-/reload` 重新加载；Grafana 面板改 `deploy/grafana/dashboards/` 中的 JSON 后自动加载（文件来源默认每 10 秒扫描一次），界面上的修改不会写回文件。
- **查看 Trace**：在 Jaeger UI 中按服务（`marschat-access` 等）和操作（`ws.SendMessage`、`consume marschat.message.events`）检索，或按标签 `marschat.conversation_id`、`marschat.seq`、`marschat.client_msg_id` 检索。Grafana 13 的 Jaeger 数据源调用的是 Jaeger 2.23 已移除的旧查询接口，所以没有配置，面板顶部的链接直接打开 Jaeger UI。
- **告警**：规则见 [observability.md](design/observability.md) 第 3 节，触发中的告警在 Alertmanager 或面板的“触发中的告警”中查看。被杀的实例在 Consul 中保持 critical 600 秒后注销，`InstanceDown` 随之结束。
- **实测内存**（2026-11-21）：空闲时 Prometheus 约 93 MiB、Alertmanager 约 21 MiB、Grafana 约 250 MiB、Jaeger 约 28 MiB；跑完集群测试和压测后 Prometheus 约 116 MiB、Jaeger 约 168 MiB（随保存的 Trace 增长），四个组件合计约 510 MiB。
- **镜像大小**（解压后）：Grafana 2.02 GB、Prometheus 389 MB、Jaeger 180 MB、Alertmanager 133 MB、k6 111 MB。

### 慢查询日志

MySQL 把执行超过 200 毫秒的语句写入数据卷中的 `/var/lib/mysql/mysql-slow.log`，阈值与 GORM 慢 SQL 日志一致；不记录未走索引的快查询。日志带参数值，只在本机查看：

```bash
make mysql-slow
```

## 压测

k6 在 profile `loadtest` 中，用 `docker compose run --rm k6` 按需运行，经网关（`http://apisix:9080`、`ws://apisix:9080/ws`）压测容器模式的服务。脚本在 [test/load/k6/](../test/load/k6/)，准备的数据（用户与令牌）写入 `tmp/load/users.json`，结果摘要写入 `tmp/load/out/`，都被 Git 忽略。k6 容器的固定 IP 与网关的 `real-ip` 规则见“网关”。

```bash
make cluster-up && make monitoring-up
make load-up                     # account 临时切到 log 发送方（固定验证码，不发真实邮件），Trace 采样比例 0.05，.env 不改
make load-seed                   # 注册 1000 个用户（LOAD_USERS），两两加好友，建一个 31 人的热点群；约 30 秒
make load-ws CONNS=500           # WebSocket 收发：每个连接每秒给好友发 1 条消息，默认 60 秒（DURATION）
make load-read RATE=500          # 热点读：用户资料、群资料、群成员、会话列表，固定到达率
make load-read MODE=penetration  # 穿透：不存在的用户 ID、群 ID
make load-bust                   # 击穿：在 load-read 运行期间删除热点群的缓存键
make load-restore                # 按 .env 重建应用容器，恢复邮件发送方与采样比例
```

- **令牌**：访问令牌 15 分钟有效，每轮压测前执行 `make load-token`（用刷新令牌换新令牌，几秒完成）。
- `make load-seed` 会先检查 account 使用 `log` 发送方，否则拒绝运行，避免向 `@example.test` 地址发送真实邮件。
- `load-ws` 的统计只取全部连接建立后的稳态窗口：`ws_ack_ms` 是发出 `SendMessage` 到收到确认，`ws_push_ms` 是接收方收到推送的时刻减去正文中的发送时刻（收发双方在同一个 k6 进程中）。阈值失败时 k6 以 99 退出（make 随之报错），摘要照常写出。
- 服务端的数据（连接数、投递延迟、Outbox 延迟、Kafka 积压、缓存命中与回源、各容器的 CPU 与内存）在 Grafana 总览面板中查看。
- 积压告警实验：`docker compose stop message` 后运行 `MSYS_NO_PATHCONV=1 docker compose run --rm k6 run /scripts/lag_events.js`，产生 2000 条 social 事件，5 分钟后 `KafkaConsumerLag` 触发；`docker compose start message` 后积压消化、告警结束。
- 压测会在数据库中留下用户、好友关系和消息（1000 个连接 60 秒约 6.5 万条），需要清理时见“停止与重置”。
- 结果与环境基线见[重构计划](plans/active/backend-refactor.md#压测结果)。

## 服务配置

配置文件在 `app/<服务>/configs/config.yaml`，结构定义在各服务的 `internal/conf/conf.proto`，约定见 [system.md](architecture/system.md#51-配置)。

- `${XXX}` 引用环境变量 `MARSCHAT_XXX`，`${XXX:默认值}` 在变量缺失时取默认值。默认值对应本地开发环境，一般只需要在 `.env` 中设置密码。
- 时长只能用秒为单位书写，例如 `0.5s`、`3600s`。

可以覆盖的环境变量：

| 变量 | 默认值 | 说明 |
|---|---|---|
| `MARSCHAT_ACCOUNT_HTTP_ADDR` / `MARSCHAT_ACCOUNT_GRPC_ADDR` | `127.0.0.1:18010` / `127.0.0.1:19010` | account 监听地址 |
| `MARSCHAT_SOCIAL_HTTP_ADDR` / `MARSCHAT_SOCIAL_GRPC_ADDR` | `127.0.0.1:18020` / `127.0.0.1:19020` | social 监听地址 |
| `MARSCHAT_MESSAGE_HTTP_ADDR` / `MARSCHAT_MESSAGE_GRPC_ADDR` | `127.0.0.1:18030` / `127.0.0.1:19030` | message 监听地址 |
| `MARSCHAT_ACCESS_HTTP_ADDR` / `MARSCHAT_ACCESS_GRPC_ADDR` | `127.0.0.1:18000` / `127.0.0.1:19100` | access 监听地址（WebSocket / 内部 gRPC）。gRPC 不用 19000：它原本留给 MinIO，ADR-0007 后不再部署 MinIO，access 仍沿用 19100 |
| `MARSCHAT_ACCESS_ADVERTISE_ADDR` | 空 | 写入在线路由的 access gRPC 地址，delivery 按它直连。留空时取注册到 Consul 的 gRPC 端点（宿主机为 `127.0.0.1:19100`，容器内为容器 IP），启动日志 `route address resolved` 中可以看到；只有经 NAT 等不同地址访问时才需要填写 |
| `MARSCHAT_DELIVERY_HTTP_ADDR` / `MARSCHAT_DELIVERY_GRPC_ADDR` | `127.0.0.1:18040` / `127.0.0.1:19040` | delivery 监听地址；HTTP 只有 `/healthz`、`/readyz`、`/metrics` |
| `MARSCHAT_MEDIA_HTTP_ADDR` / `MARSCHAT_MEDIA_GRPC_ADDR` | `127.0.0.1:18050` / `127.0.0.1:19050` | media 监听地址 |
| `MARSCHAT_KAFKA_BROKERS` | `127.0.0.1:19092` | Kafka 地址，逗号分隔，`make e2e` 也读取它 |
| `MARSCHAT_MYSQL_ADDR` | `127.0.0.1:13306` | MySQL 地址 |
| `MARSCHAT_REDIS_ADDR` | `127.0.0.1:16379` | Redis 地址 |
| `MARSCHAT_CONSUL_ADDR` | `127.0.0.1:18500` | Consul 地址，`make e2e` 也读取它 |
| `MARSCHAT_CONSUL_TCP_CHECK_INTERVAL` | `0s` | Consul 主动 TCP 探测服务端口的间隔，同时是 TTL 心跳间隔（超时为 2 倍），只取整秒。`0s` 表示不做 TCP 探测、心跳间隔 10 秒；容器模式为 `3s` |
| `MARSCHAT_LOG_LEVEL` | `info` | `debug`、`info`、`warn`、`error` |
| `MARSCHAT_LOG_FORMAT` | `text` | `text` 或 `json` |
| `MARSCHAT_OTLP_ENDPOINT` | 空 | Trace 的 OTLP gRPC 导出地址，空表示不导出（指标不受影响）；容器模式为 `jaeger:4317`，宿主机运行时可以设为 `127.0.0.1:14317` |
| `MARSCHAT_TRACE_SAMPLE_RATIO` | `1` | Trace 采样比例（0～1），上游已经决定的请求跟随上游；compose 中可以在命令的环境里覆盖，压测时用 `0.05`（`make load-up`） |
| `MARSCHAT_JWT_KEY_ID` | `dev-1` | 访问令牌头部的 `kid`，为轮换密钥预留 |
| `MARSCHAT_TRUSTED_PROXY` | 空 | account 信任的代理（网关）IP 或 CIDR，只有直接连接方是它时才读取 `X-Forwarded-For`；容器模式为网关的固定 IP `172.30.0.10` |
| `MARSCHAT_MAIL_PROVIDER` | `log` | 验证码邮件的发送方：`log` 只写日志，`smtp` 经 SMTP 真实发信，见“验证码邮件” |
| `MARSCHAT_MAIL_FIXED_CODE` | `000000` | 开发与 e2e 用的固定验证码，只允许和 `log` 发送方一起使用；置空则随机生成 6 位（`log` 发送方写入日志） |
| `MARSCHAT_SMTP_ADDR` / `MARSCHAT_SMTP_USERNAME` / `MARSCHAT_SMTP_PASSWORD` | 空 | SMTP 服务器 `host:port`、发件邮箱（同时是登录名）、SMTP 授权码，`smtp` 发送方必填 |
| `MARSCHAT_ACCOUNT_ENDPOINT` | `discovery:///marschat-account` | social、message 调用 account 的 gRPC 地址；也可以写 `127.0.0.1:19010` 这样的直连地址 |
| `MARSCHAT_SOCIAL_ENDPOINT` | `discovery:///marschat-social` | message、delivery、access 调用 social 的地址 |
| `MARSCHAT_MESSAGE_ENDPOINT` | `discovery:///marschat-message` | access 调用 message 的地址 |
| `MARSCHAT_MEDIA_ENDPOINT` | `discovery:///marschat-media` | message 调用 media 的地址 |
| `MARSCHAT_STORAGE_PROVIDER` | `local` | media 的文件存储：`local` 或 `oss`，见“文件存储” |
| `MARSCHAT_MEDIA_LOCAL_DIR` | `data/media` | 本地存储目录，相对路径相对于工作目录；容器模式为 `/data/media` |
| `MARSCHAT_MEDIA_PUBLIC_BASE_URL` | `http://127.0.0.1:18050` | 本地存储下载地址的前缀；容器模式为网关地址 `https://127.0.0.1:19443` |
| `MARSCHAT_MEDIA_URL_SECRET` | 空 | 本地存储下载地址的签名密钥，`make keys` 生成 |
| `MARSCHAT_OSS_REGION`、`MARSCHAT_OSS_BUCKET`、`MARSCHAT_OSS_ACCESS_KEY_ID`、`MARSCHAT_OSS_ACCESS_KEY_SECRET` | 空 | OSS 地域、Bucket 和 RAM 用户的 AccessKey，`oss` 存储必填 |
| `MARSCHAT_OSS_ENDPOINT`、`MARSCHAT_OSS_KEY_PREFIX`、`MARSCHAT_OSS_PUBLIC_ENDPOINT` | 空、`marschat/`、空 | OSS 访问地址（留空按地域取公网地址）、对象键前缀、绑定的自定义域名 |

宿主机运行时不做 TCP 探测（`registry.consul.tcp_check_interval` 为 `0s`），原因见 [system.md](architecture/system.md#53-服务注册与发现)。

account 的主要配置段（默认值见 [config.yaml](../app/account/configs/config.yaml)）：

| 配置段 | 内容 | 生效方式 |
|---|---|---|
| `auth` | 公钥、签发方、受众（各服务相同） | 重启 |
| `token` | 私钥、`kid`、访问令牌 900 秒、刷新令牌 30 天、轮换宽限期 10 秒、吊销记录保留 1 小时（不得小于访问令牌有效期） | 重启 |
| `mail` | 发送方（`log` 或 `smtp`）、固定验证码；`smtp` 段为服务器地址、用户名、授权码和单次发送超时 2.5 秒（不超过 10 秒，同时不超过请求的剩余时间） | 重启 |
| `security` | bcrypt cost（10～14，默认 12）、可信代理 `trusted_proxies`（`${TRUSTED_PROXY:}`，宿主机运行为空，容器模式为网关 IP） | 重启 |
| `cache` | 资料缓存 1800 秒、空值标记 60 秒 | 重启 |
| `kafka`、`outbox` | Kafka 地址；Outbox 轮询间隔 0.5 秒（事务提交后会立即唤醒，轮询只是兜底）、每批 200 条、已发布事件保留 7 天 | 重启 |
| `policy` | 邮箱验证码：有效期 300 秒、验证 5 次、发送间隔 60 秒、每个邮箱每日 10 封、每 IP 每小时 20 次；登录：15 分钟内错 5 次锁定 15 分钟、每 IP 每分钟 20 次 | **热更新**：保存配置文件后自动生效，日志出现 `config reloaded key=policy`；值不合法时保留旧值并记录 ERROR |

social 的主要配置段（默认值见 [config.yaml](../app/social/configs/config.yaml)）：

| 配置段 | 内容 | 生效方式 |
|---|---|---|
| `auth` | 同 account | 重启 |
| `account` | account 地址、调用超时 1 秒 | 重启 |
| `cache` | 群资料 1800 秒、成员列表 1800 秒、空值标记 60 秒 | 重启 |
| `kafka`、`outbox` | 同 account；`kafka.session_timeout` 为消费组会话超时 10 秒（实例被杀后，它的分区要等这么久才转给其他实例） | 重启 |
| `policy` | 每人每小时 20 条好友申请、每人每天建 10 个群、每人每小时 20 次入群、群成员上限 500、建群初始成员上限 50 | **热更新**，同 account；调低群成员上限不会移除已有成员 |

message 的主要配置段（默认值见 [config.yaml](../app/message/configs/config.yaml)）：

| 配置段 | 内容 | 生效方式 |
|---|---|---|
| `auth`、`kafka`、`outbox` | 同 account；`kafka.session_timeout` 同 social | 重启 |
| `social`、`account`、`media` | 地址、调用超时 1 秒 | 重启 |
| `policy` | 每人每 10 秒最多发 30 条消息（所有设备、所有会话合计）、`GetDifference` 一次最多 200 条、缺口超过 1000 条返回 `TOO_LONG` | **热更新** |

access 的主要配置段（默认值见 [config.yaml](../app/access/configs/config.yaml)）：

| 配置段 | 内容 | 生效方式 |
|---|---|---|
| `auth` | 同 account | 重启 |
| `advertise_addr` | 写入在线路由的本实例 gRPC 地址，默认留空，取注册的 gRPC 端点 | 重启 |
| `message` | 地址、调用超时 3 秒 | 重启 |
| `social` | 地址、调用超时 1 秒，发起呼叫前校验权限 | 重启 |
| `call` | 通话信令：`ring_timeout` 振铃超时 60 秒（5～300 秒）；`forward_timeout` access 之间转发信令的超时 2 秒；`forward_cooldown` 对端 access 实例不可达后的冷却期 5 秒（0 表示只摘除连接） | 重启 |
| `websocket` | 认证超时 10 秒、心跳间隔 30 秒、心跳超时 90 秒（必须大于心跳间隔，同时是路由 TTL）、发送队列 256、写超时 10 秒、最大帧 64 KiB；`drain_window` 停机时分批关闭连接的窗口 5 秒（0～10 秒，0 表示一次全部关闭；进程停机超时固定为 15 秒） | 重启 |
| `policy` | 每个连接每秒最多 30 个上行帧 | **热更新** |

delivery 的主要配置段（默认值见 [config.yaml](../app/delivery/configs/config.yaml)）：

| 配置段 | 内容 | 生效方式 |
|---|---|---|
| `server.http` | 探针与 `/metrics`（步骤 8 新增），Consul 中登记的地址随之变为 HTTP 端点 | 重启 |
| `kafka` | Kafka 地址；`session_timeout` 同 social；积压统计（`marschat_kafka_consumer_lag`）也用它，每 15 秒查询一次 | 重启 |
| `social` | 地址、调用超时 1 秒 | 重启 |
| `push` | 调用 access `Push`、`Kick` 的超时 2 秒 | 重启 |
| `policy` | `push_cooldown`：access 实例不可达（`Unavailable` 或超时）后的冷却期 5 秒（0～60 秒），期间发往它的推送直接跳过，日志出现 `access instance unreachable, connection evicted`；0 表示只摘除连接、下一次立即重新拨号 | **热更新** |

各服务都有 `telemetry` 段：`otlp_endpoint`（`${OTLP_ENDPOINT:}`）和 `sample_ratio`（`${TRACE_SAMPLE_RATIO:1}`），修改后重启。指标总是在 HTTP 端口的 `/metrics` 上暴露，与这一段无关。

media 的主要配置段（默认值见 [config.yaml](../app/media/configs/config.yaml)）：

| 配置段 | 内容 | 生效方式 |
|---|---|---|
| `server` | HTTP 超时 300 秒（Kratos 对所有路由使用同一个超时，要覆盖 100 MB 上传） | 重启 |
| `auth` | 同 account | 重启 |
| `storage` | `provider`（`local` 或 `oss`）及对应的子段，见“文件存储” | 重启 |
| `cache` | 文件元数据 1800 秒、空值标记 60 秒 | 重启 |
| `policy` | 聊天文件上限 100 MiB、生成缩略图的图片上限 10 MiB、头像上限 5 MiB、图片像素上限 1600 万、缩略图最长边 320 像素、下载地址有效期 600 秒、每人每小时上传 60 次 | **热更新**；调大聊天文件上限超过 101 MiB 时，要同步调大网关 media 路由的 `client-control.max_body_size` |

### 验证码邮件

注册和验证码登录的验证码发到邮箱。默认的 `log` 发送方只把验证码写进 account 日志（`msg="verification code (log provider, dev only)"`），开发和自动化测试使用固定验证码 `000000`；生产环境必须使用 `smtp`。设计见 [email-verification.md](design/email-verification.md)。

用个人邮箱真实发信：

1. 在邮箱网页版的设置中开启 SMTP 服务并生成**授权码**（QQ 邮箱：设置 → 账号 → POP3/IMAP/SMTP 服务；163 邮箱：设置 → POP3/SMTP/IMAP）。授权码只用于第三方客户端登录，不是邮箱密码；泄露后在同一页面作废重新生成即可。
2. 在 `.env` 中设置（变量名见 [.env.example](../.env.example)，授权码只写在 `.env`）：

   ```bash
   MARSCHAT_MAIL_PROVIDER=smtp
   MARSCHAT_MAIL_FIXED_CODE=          # 必须置空，固定验证码只允许和 log 发送方一起使用
   MARSCHAT_SMTP_ADDR=smtp.qq.com:587 # STARTTLS；其他邮箱的地址与端口以其帮助页面为准
   MARSCHAT_SMTP_USERNAME=你的邮箱地址
   MARSCHAT_SMTP_PASSWORD=SMTP 授权码
   ```

3. 重启 account（宿主机 `make run-account`；容器模式 `docker compose --profile apps up -d account`，compose 会把这些变量传进容器）。配置不完整（缺用户名或授权码、用户名不是邮箱地址、地址不是 `host:port`）时启动失败。

说明：

- 465 端口使用隐式 TLS，其他端口（如 587）要求服务器支持 STARTTLS，不支持时拒绝发送；不会经明文连接传输授权码。服务器证书按系统根证书校验。
- 日志报 `tls: failed to verify certificate: x509: certificate signed by unknown authority` 时，说明到 SMTP 服务器的 TLS 连接被截获、证书被替换。开发机上 465 端口出现过这种情况，587 正常，排查方法见 [lessons/smtp-465-tls-interception.md](lessons/smtp-465-tls-interception.md)。不要关闭证书校验。
- 修改 `.env` 中的邮件配置后要重建 account 才生效：容器模式 `docker compose --profile apps up -d account`，宿主机重新 `make run-account`。
- 2026-11-20 实测（QQ 邮箱，587）：一次发送从请求到 SMTP 服务器接受邮件 1.2～1.5 秒，在 2.5 秒的发送超时以内。
- 发往保留域名（`.test`、`.example`、`.invalid`、`.localhost`、`example.com/net/org`）的验证码直接按发送失败处理，不占用个人邮箱的每日发信额度。e2e 的测试账号（`@example.test`）和迁移填充的占位邮箱（`@legacy.invalid`）都在其中。
- 发送失败（连接、认证、服务器拒收、超时）返回 `503 CODE_SEND_FAILED`，撤销本次的发送间隔和每日计数，可以立即重试；原因写在 account 的 ERROR 日志 `send verification code failed` 中（邮箱已脱敏）。
- 运行 `make e2e`、`make cluster-test` 前把 `MARSCHAT_MAIL_PROVIDER` 改回 `log`（或注释掉），`MARSCHAT_MAIL_FIXED_CODE` 恢复为 `000000`：测试依赖固定验证码。

### 文件存储

media 的文件存储由 `storage.provider` 选择，设计见 [media-service.md](design/media-service.md)，选型见 [ADR-0007](architecture/adr/0007-object-storage.md)。

**本地目录（默认，`local`）**：文件写在仓库下的 `data/media/`（被 Git 忽略，media 启动时创建），下载经 media 的签名地址。

- 目录结构：`files/<两位十六进制>/<文件 ID>` 为原文件，`thumbs/<两位十六进制>/<文件 ID>.jpg` 为缩略图，`tmp/` 为写入中的临时文件。
- 签名密钥 `MARSCHAT_MEDIA_URL_SECRET` 由 `make keys` 生成（至少 32 个字符），缺失或过短时 media 启动失败。更换后已签发的下载地址失效，客户端重新换取即可，文件不受影响。
- 下载地址前缀 `MARSCHAT_MEDIA_PUBLIC_BASE_URL`：宿主机运行为 `http://127.0.0.1:18050`（直连 media）；容器模式为 `https://127.0.0.1:19443`（经网关）。客户端访问的地址变化时要同步修改。
- 容器模式把 `./data/media` 挂载到 media 容器的 `/data/media`，宿主机运行和容器模式共用同一份文件。media 只有一个实例；扩到多个实例时它们必须共享同一个目录。

**阿里云 OSS（`oss`）**：上传经 media 写入 OSS，下载用 OSS 的 V4 预签名地址，客户端直连 OSS，不经过 media。

1. 在 OSS 控制台创建 Bucket，读写权限选**私有**。
2. 在 RAM 控制台创建 RAM 用户，只授予该 Bucket 的读写权限（`oss:PutObject`、`oss:GetObject`、`oss:DeleteObject`，资源限定为 `acs:oss:*:*:<bucket>/*`），生成 AccessKey。不要使用主账号的 AccessKey。
3. 在 `.env` 中设置（变量名见 [.env.example](../.env.example)，AccessKey 只写在 `.env`）：

   ```bash
   MARSCHAT_STORAGE_PROVIDER=oss
   MARSCHAT_OSS_REGION=cn-hangzhou          # Bucket 所在地域
   MARSCHAT_OSS_BUCKET=<bucket 名称>
   MARSCHAT_OSS_ACCESS_KEY_ID=<RAM 用户的 AccessKey ID>
   MARSCHAT_OSS_ACCESS_KEY_SECRET=<RAM 用户的 AccessKey Secret>
   ```

4. 重启 media：宿主机重新 `make run-media`；容器模式 `docker compose --profile apps up -d media`。配置不完整时启动失败并列出缺少的项。启动时不访问 OSS，AccessKey 错误或 Bucket 不存在要到第一次上传时才会暴露（返回 `503 DEPENDENCY_UNAVAILABLE`，OSS 的错误码见 media 的请求日志）。

说明：

- `MARSCHAT_OSS_ENDPOINT` 留空时按地域使用公网地址；部署在同地域 ECS 上时可以填内网地址，节省流量费用。`MARSCHAT_OSS_KEY_PREFIX` 是对象键前缀（默认 `marschat/`），多个环境共用一个 Bucket 时用它区分。
- 2019 年 9 月 23 日之后创建的 Bucket，用默认域名在浏览器标签页中直接打开图片会被强制下载；要预览时为 Bucket 绑定已备案的自定义域名，填入 `MARSCHAT_OSS_PUBLIC_ENDPOINT`（例如 `https://files.example.com`），预签名地址改用该域名。`<img>` 嵌入是否受影响在 OSS 手动验证中确认（尚未执行）。
- 前端以后用 `fetch` 读取文件内容（而不只是 `<img>` 显示、链接下载）时，需要在 Bucket 上配置跨域（CORS）规则。
- OSS 按存储量、请求次数和公网下行流量计费。开发时用完记得清理 Bucket，或者切回 `local`。
- **切换存储不迁移已有文件**：每个文件记录了它写入时的存储，切换后，写在另一种存储中的文件在换取下载地址时被省略（客户端看到的是文件不可用），新上传的文件写入新存储。切回原来的存储，这些文件恢复可用。

## 开发命令

| 命令 | 作用 |
|---|---|
| `make gen` | 重新生成配置 proto（`make conf`）、接口 proto（`make api`）和依赖注入代码（`make wire`） |
| `make lint` | `buf lint` |
| `make vet` / `make test` | `go vet ./...` / `go test ./...` |
| `make build` | 构建全部服务到 `bin/`，版本号取 `git describe` |
| `make e2e` | 端到端测试，需要依赖组件已启动、account、social、message、media 已迁移，六个服务都在运行（media 使用默认的本地存储，测试文件写入 `data/media/`）；会加载 `.env`，并清理本机 IP（127.0.0.1）的限流计数。其中 `bloom_test.go` 直接连 Redis，用随机键前缀测试布隆过滤器，结束后清理；`kafka_test.go` 使用专用 topic `marschat.e2e.events`、`marschat.e2e.retry` 和随机消费组，并在死信队列中查找本次写入的记录 |
| `make images` | 宿主机交叉编译全部服务并构建镜像 `marschat/<服务>:dev`，见“容器模式” |
| `make certs` | 生成网关的自签证书到 `deploy/apisix/certs/`（已有时不覆盖） |
| `make cluster-up` / `make cluster-down` | 启动容器模式的全部服务和网关 / 删除这些容器（依赖组件保持运行） |
| `make cluster-test` | 集群测试与故障实验，需要容器模式已启动；客户端只经网关访问，清理网关来源 IP 的限流计数。故障实验会停止、暂停、重启 MarsChat 的容器，见“容器模式” |
| `make keys` | 生成 JWT 密钥对和 media 下载地址签名密钥写入 `.env`（已有的不覆盖） |
| `make migrate-<服务>` | 数据库迁移，`ARGS` 传子命令 |
| `make monitoring-up` / `make monitoring-down` | 启动监控组件 / 删除它们的容器（保留 Prometheus 数据卷），见“监控” |
| `make rules-test` | 用 Prometheus 镜像中的 promtool 校验配置、运行告警规则的单元测试，不需要启动监控组件 |
| `make mysql-slow` | 查看慢查询日志的最后 50 行 |
| `make load-up`、`load-seed`、`load-token`、`load-ws`、`load-read`、`load-bust`、`load-restore` | 压测，见“压测” |
| `make grant-admin EMAIL=…` | 设置管理员 |

CI（`.github/workflows/ci.yml`）在推送到 `main`、`dev` 和提交 PR 时执行：`buf lint`、`buf breaking`（PR 与基准分支比较，推送与上一次推送比较）、重新生成代码并检查与提交一致、`go mod tidy -diff`、`go vet`、`go build`、`go test -race`。e2e 需要 compose 环境，暂不在 CI 中运行。

## 死信队列

消费者处理不了的记录写入 `marschat.dlq`，保留 14 天，目前没有重新投递工具。用 Kafka 自带的命令带头部查看（Git Bash 中需要 `MSYS_NO_PATHCONV=1`）：

```bash
MSYS_NO_PATHCONV=1 docker compose exec kafka /opt/kafka/bin/kafka-console-consumer.sh \
  --bootstrap-server kafka:29092 --topic marschat.dlq --from-beginning --timeout-ms 5000 \
  --property print.headers=true --property print.key=true --property print.timestamp=true
```

- 头 `x-dlq-group` 是写入的消费组，`x-dlq-topic`、`x-dlq-partition`、`x-dlq-offset` 是原记录的位置，`x-dlq-error` 是失败原因（最长 4 KiB）；`event_type`、`event_id` 等原始头保留。
- 值是事件的 protobuf 编码，控制台中显示为乱码，按 `event_type` 解码。
- 写入时服务日志有一条 ERROR：`kafka event failed permanently, moving to dead letter topic`。

## 停止与重置

```bash
make down    # 停止并移除容器和网络，保留数据卷
```

需要清空全部数据（例如修改了建库脚本或密码）时，先确认没有要保留的数据，再删除本项目的数据卷：

```bash
docker compose down -v
make up
```

`-v` 只删除 `compose.yaml` 中声明的 `marschat_*` 卷，不影响其他项目。

media 本地存储的文件在仓库的 `data/media/` 目录中，不在数据卷里。清空数据库后这些文件不再有记录引用，确认不需要后手动删除该目录；使用 OSS 时在控制台清理 Bucket 中 `marschat/` 前缀下的对象。
