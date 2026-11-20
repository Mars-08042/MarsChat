# 部署与运行

> 本文描述**新后端**的运行方式，随重构步骤更新，最近一次更新于步骤 5（2026-11-20）。服务有两种运行方式：宿主机运行（日常开发）和容器模式（多实例实验，经网关访问）。旧单体的部署流程见提交 `6624cea` 中的本文。

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
| MySQL | `mysql:9.7.4` | `127.0.0.1:13306` | 首次初始化时为每个服务建库建账号，见 [data-model.md](architecture/data-model.md#1-数据库与账号) |
| Redis | `redis:8.8.4` | `127.0.0.1:16379` | 开启 AOF 持久化和密码；加载镜像自带的 RedisBloom 模块（用户、群 ID 布隆过滤器）。**最低 8.0**：在线路由用 `HSETEX` 设置 Hash 字段级 TTL |
| Kafka | `apache/kafka:4.4.0` | `127.0.0.1:19092` | 单节点 KRaft；容器网络内地址为 `kafka:29092`；关闭自动建 topic，topic 由 `kafka-init` 显式创建；堆内存限制为 512 MiB（镜像默认 1 GiB，Docker Desktop 虚拟机只有 3.8 GiB） |
| topic 初始化 | `apache/kafka:4.4.0` | — | `kafka-init` 一次性任务，带 profile，由 `make up` 在依赖组件健康后执行：`kafka-topics.sh --create --if-not-exists` 创建 `marschat.account.events`（3 分区）、`marschat.social.events`（6）、`marschat.message.events`（12）、重试队列 `marschat.social-cache.retry`（3）、死信队列 `marschat.dlq`（3，保留 14 天），以及 e2e 专用的 `marschat.e2e.events`、`marschat.e2e.retry`（各 3），副本数 1；重复执行不会报错，已有环境新增 topic 时执行一次 `docker compose run --rm kafka-init` |
| Consul | `hashicorp/consul:2.1.0` | `127.0.0.1:18500` | 单节点 server，带 Web UI |
| 迁移任务 | `migrate/migrate:v4.20.1` | — | `migrate-<服务>` 一次性任务，带 profile，`make up` 不启动，见“数据库迁移” |

后续步骤引入的组件和全部应用端口见 [重构计划的端口表](plans/active/backend-refactor.md#端口)。

## 首次配置

在项目根目录（Git Bash）执行：

```bash
cp .env.example .env
make keys    # 生成 JWT 密钥对并追加到 .env，已有时不覆盖，不打印密钥
```

把 `.env` 中的 `change-me` 换成随机密码，只用字母和数字，避免在 SQL 和 DSN 中转义。`.env` 被 Git 忽略，同时供 Compose 和本机运行的服务使用。

| 变量 | 用途 |
|---|---|
| `MARSCHAT_MYSQL_ROOT_PASSWORD` | MySQL root 密码 |
| `MARSCHAT_ACCOUNT_DB_PASSWORD`、`MARSCHAT_SOCIAL_DB_PASSWORD`、`MARSCHAT_MESSAGE_DB_PASSWORD`、`MARSCHAT_MEDIA_DB_PASSWORD` | 各服务数据库账号的密码 |
| `MARSCHAT_REDIS_PASSWORD` | Redis 密码 |
| `MARSCHAT_JWT_PRIVATE_KEY` | Ed25519 私钥种子（32 字节）的 base64，只有 account 使用。由 `make keys` 生成，更换后已签发的令牌全部失效 |
| `MARSCHAT_JWT_PUBLIC_KEY` | 对应的公钥 base64，所有服务用来校验访问令牌；account 启动时检查它与私钥匹配 |

MySQL 的建库脚本和 root 密码只在数据卷**首次初始化**时生效，之后修改 `.env` 不会改变已有账号的密码。需要重置时见“停止与重置”。

**Docker Desktop 文件共享**：本机 Docker Desktop 使用 Hyper-V 后端，挂载宿主机目录前需要在 Settings → Resources → File sharing 中加入该目录，否则 Docker Desktop 会弹窗询问，确认前 `docker compose up` 会一直卡住。建议直接加入项目根目录，后续 APISIX、Prometheus 的配置挂载也会用到。WSL 2 后端不需要这一步。排查过程见 [lessons/docker-desktop-bind-mount-hang.md](lessons/docker-desktop-bind-mount-hang.md)。

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
make migrate-account ARGS="down 1"      # 回滚一步
make migrate-account ARGS="version"     # 查看当前版本
```

迁移记录在各库的 `schema_migrations` 表。首次运行会拉取 `migrate/migrate` 镜像；迁移目录通过 bind mount 挂载，Docker Desktop 需要共享项目根目录（见“首次配置”）。

## 运行服务

每个服务一个终端：

```bash
make run-account    # HTTP 127.0.0.1:18010，gRPC 127.0.0.1:19010
make run-social     # HTTP 127.0.0.1:18020，gRPC 127.0.0.1:19020
make run-message    # HTTP 127.0.0.1:18030，gRPC 127.0.0.1:19030
make run-access     # WebSocket ws://127.0.0.1:18000/ws，内部 gRPC 127.0.0.1:19100
make run-delivery   # gRPC 127.0.0.1:19040（只用于注册和健康检查）
```

`make run-<服务>` 会加载 `.env`，再执行 `go run ./app/<服务>/cmd/<服务> -conf app/<服务>/configs`。服务启动时连接本服务的 MySQL（access、delivery 没有）、Redis 并注册到 Consul，任一依赖不可用会直接退出。首次启动前要先执行对应的 `make migrate-<服务>`。

服务之间经 Consul 发现，启动顺序不限。被调用的服务或 Kafka 不可用时，调用方照常启动并降级运行：

| 不可用 | 影响 |
|---|---|
| account | social 申请好友返回 `503 DEPENDENCY_UNAVAILABLE`；social、message 的列表不带昵称和头像，记 WARN |
| social | 发消息返回 `503 DEPENDENCY_UNAVAILABLE`（无法校验发言权限）；会话列表不带群资料；群消息不推送 |
| message | WebSocket 连接照常建立和接收推送，发消息返回 `503 UPSTREAM_UNAVAILABLE` |
| access | 客户端无法建立长连接，HTTP 接口不受影响 |
| delivery 或 Kafka | 消息照常写入、可以补拉，但没有实时推送和事件踢线（心跳兜底，最迟 30 秒断开被吊销的连接）；事件积压在 Outbox 表中，恢复后补发 |

在终端按 Ctrl+C 停止服务。按 Kratos 的停机流程，服务会先从 Consul 注销再退出（宿主机上未在 Windows 终端中实测；容器模式下 `docker stop` 发送 SIGTERM，已确认会先注销）。进程被强制结束时来不及注销，Consul 中的实例在 20 秒内变为 critical、不再被发现，10 分钟后自动删除。

检查服务状态：

```bash
curl -s http://127.0.0.1:18010/healthz   # 存活探针
curl -s http://127.0.0.1:18010/readyz    # 就绪探针，检查 MySQL 和 Redis
make e2e                                 # 全部服务的端到端测试，见“开发命令”
```

调用 account 的 HTTP 接口时要带 `Content-Type: application/json`，否则请求体按表单解析。开发环境短信验证码固定为 `000000`（只写日志的发送方，见下方配置），例如：

```bash
curl -s -X POST http://127.0.0.1:18010/v1/auth/sms-codes \
  -H 'Content-Type: application/json' \
  -d '{"phone":"13800000001","purpose":"SMS_PURPOSE_REGISTER"}'
```

接口列表见 [api/openapi.yaml](../api/openapi.yaml)。Git Bash 中 curl 的命令行参数按系统代码页传递，请求体里直接写中文会被服务端判为非法 UTF-8，测试中文时把请求体放进 UTF-8 文件再用 `-d @文件名`。

### 设置管理员

系统不提供设置管理员的接口。用户注册后执行：

```bash
make grant-admin PHONE=13800000001
```

脚本 [scripts/grant-admin.sh](../scripts/grant-admin.sh) 在 MySQL 容器中把该用户的 `role` 改为管理员并删除资料缓存。新角色写在访问令牌里，该用户下次刷新令牌或重新登录后生效。

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
| delivery | 2 | gRPC 19040 |
| access | 2 | HTTP 18000（WebSocket）、gRPC 19100 |
| apisix（网关） | 1 | 9080、9443，发布到宿主机 `127.0.0.1:19080`、`127.0.0.1:19443`；固定 IP `172.30.0.10` |

- **与宿主机运行互斥**：两种模式共用 Consul 且服务名相同，宿主机实例注册 `127.0.0.1`，容器访问不到；容器实例注册容器 IP，宿主机访问不到。切换前先停掉另一种。宿主机进程被强制结束时，它在 Consul 中的实例要 20 秒后才变为 critical，这段时间内不要启动容器模式。
- **不发布宿主机端口**：应用容器只在 Compose 网络内可访问，客户端入口是网关（见下节）。依赖组件的宿主机端口照常可用。
- **镜像**：运行镜像为 `gcr.io/distroless/static-debian13:nonroot`，按 digest 锁定（见 [Dockerfile](../deploy/docker/Dockerfile)），没有 shell，不能 `docker exec` 进去排查；需要在网络内发请求时，临时起一个带 curl 的容器加入 `marschat_default` 网络。二进制默认按 amd64 编译，ARM 机器用 `make images IMAGE_ARCH=arm64`。
- **注册与健康检查**：容器内开启 Consul 的 TCP 检查，间隔 3 秒（`MARSCHAT_CONSUL_TCP_CHECK_INTERVAL`），TTL 心跳超时 6 秒，实例被杀后几秒内从发现结果中消失。HTTP 端点排在第一个，Consul 中服务的 `Address:Port` 是 HTTP 端口，网关按它转发。
- **资源**：每个应用容器限制 256 MiB 内存。2026-11-19 实测：应用容器各 15～56 MiB，Kafka 512 MiB、MySQL 452 MiB，MarsChat 合计约 1.2 GiB。
- **日志**：JSON 格式，`docker compose logs -f access` 查看；同一服务的多个实例日志会合并输出，容器名区分实例。
- **配置热更新**：配置目录挂载自宿主机，修改 `policy` 段后容器内同样自动生效（2026-11-19 验证，0.5 秒内出现 `config reloaded`）。
- **故障实验**：`make cluster-test` 包含 7 项故障实验，全部运行约 4 分钟。实验会对 MarsChat 自己的容器执行 `docker kill`、`restart`、`pause`、`stop`（access、delivery、message、Kafka、Redis），结束时（包括失败）恢复并等待其在 Consul 中恢复 passing 或健康检查通过。实验期间不要同时使用这套环境。只跑集群测试时加 `-short`：`set -a && . ./.env && set +a && go test -tags cluster -short -v ./test/cluster/...`。实验结果见[重构计划](plans/active/backend-refactor.md#故障实验结果)。

### 网关

APISIX `3.20.0`（镜像 `apache/apisix:3.20.0-debian`），Standalone 模式：不依赖 etcd，没有 Admin API，配置全部来自 [deploy/apisix/](../deploy/apisix/)。

| 文件 | 内容 | 生效方式 |
|---|---|---|
| `config.yaml` | 监听端口、Consul 发现、启用的插件、worker 数（2） | 重启容器 |
| `apisix.yaml` | 路由、上游、证书、限流，文件以 `#END` 结尾 | **热更新**：保存后约 1 秒内生效，日志出现 `apisix.yaml reloaded` |
| `certs/` | `make certs` 生成的自签证书（`localhost`、`127.0.0.1`），被 git 忽略 | 重启容器 |

路由（未列出的路径由网关返回 404，`/healthz`、`/readyz` 不对外）：

| 路径 | 上游（Consul 服务名） |
|---|---|
| `/v1/auth/*`、`/v1/users/*`、`/v1/sessions`、`/v1/sessions/*`、`/v1/admin/*` | `marschat-account` |
| `/v1/friend-requests[/*]`、`/v1/friends[/*]`、`/v1/blocks[/*]`、`/v1/groups[/*]`、`/v1/group-join-requests/*` | `marschat-social` |
| `/v1/conversations[/*]` | `marschat-message` |
| `/ws`（WebSocket） | `marschat-access`（`least_conn`，读超时 120 秒） |

- **服务发现**：按服务名取 Consul 中检查通过的实例，节点为服务的 `Address:Port`（HTTP 端口）。阻塞查询，实例变化约 1 秒内同步。HTTP 上游连接失败时换节点重试一次。2026-11-19 实测：杀掉一个 message 实例后连续请求没有失败，只有约 5 秒内的两个请求因 2 秒连接超时后重试而变慢；实例重启后约 2.5 秒重新接到流量。
- **入口限流**（按客户端 IP，超限返回 429）：全部 HTTP 接口每秒 50 次、突发 100；`/v1/auth/*` 另加每分钟 60 次；`/ws` 每个 IP 最多 50 条并发连接、每秒 10 次握手。计数在网关进程内。
- **客户端 IP**：经 Docker Desktop 端口转发访问时，本机所有请求的来源地址都是 compose 网络的网关 `172.30.0.128`，限流对本机流量相当于全局限流；account 看到的客户端 IP 也是它，集群测试清理的是这个 IP 的限流计数。
- **TLS**：`https://localhost:19443`、`wss://localhost:19443/ws`；用 IP 访问时按 `localhost` 匹配证书。证书经启动命令读入环境变量，由 `apisix.yaml` 用 `$env://` 引用。curl 需要指定证书：

```bash
curl --cacert deploy/apisix/certs/tls.crt https://localhost:19443/v1/users/me
```

- **日志**：`docker compose logs -f apisix`，访问日志和错误日志都输出到容器的标准输出。

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
| `MARSCHAT_ACCESS_HTTP_ADDR` / `MARSCHAT_ACCESS_GRPC_ADDR` | `127.0.0.1:18000` / `127.0.0.1:19100` | access 监听地址（WebSocket / 内部 gRPC）。gRPC 不用 19000：它留给步骤 6 的 MinIO |
| `MARSCHAT_ACCESS_ADVERTISE_ADDR` | 空 | 写入在线路由的 access gRPC 地址，delivery 按它直连。留空时取注册到 Consul 的 gRPC 端点（宿主机为 `127.0.0.1:19100`，容器内为容器 IP），启动日志 `route address resolved` 中可以看到；只有经 NAT 等不同地址访问时才需要填写 |
| `MARSCHAT_DELIVERY_GRPC_ADDR` | `127.0.0.1:19040` | delivery 监听地址 |
| `MARSCHAT_KAFKA_BROKERS` | `127.0.0.1:19092` | Kafka 地址，逗号分隔，`make e2e` 也读取它 |
| `MARSCHAT_MYSQL_ADDR` | `127.0.0.1:13306` | MySQL 地址 |
| `MARSCHAT_REDIS_ADDR` | `127.0.0.1:16379` | Redis 地址 |
| `MARSCHAT_CONSUL_ADDR` | `127.0.0.1:18500` | Consul 地址，`make e2e` 也读取它 |
| `MARSCHAT_CONSUL_TCP_CHECK_INTERVAL` | `0s` | Consul 主动 TCP 探测服务端口的间隔，同时是 TTL 心跳间隔（超时为 2 倍），只取整秒。`0s` 表示不做 TCP 探测、心跳间隔 10 秒；容器模式为 `3s` |
| `MARSCHAT_LOG_LEVEL` | `info` | `debug`、`info`、`warn`、`error` |
| `MARSCHAT_LOG_FORMAT` | `text` | `text` 或 `json` |
| `MARSCHAT_JWT_KEY_ID` | `dev-1` | 访问令牌头部的 `kid`，为轮换密钥预留 |
| `MARSCHAT_TRUSTED_PROXY` | 空 | account 信任的代理（网关）IP 或 CIDR，只有直接连接方是它时才读取 `X-Forwarded-For`；容器模式为网关的固定 IP `172.30.0.10` |
| `MARSCHAT_SMS_FIXED_CODE` | `000000` | 开发与 e2e 用的固定验证码，只允许和 `log` 发送方一起使用；置空则随机生成 6 位并写入日志 |
| `MARSCHAT_ACCOUNT_ENDPOINT` | `discovery:///marschat-account` | social、message 调用 account 的 gRPC 地址；也可以写 `127.0.0.1:19010` 这样的直连地址 |
| `MARSCHAT_SOCIAL_ENDPOINT` | `discovery:///marschat-social` | message、delivery 调用 social 的地址 |
| `MARSCHAT_MESSAGE_ENDPOINT` | `discovery:///marschat-message` | access 调用 message 的地址 |

宿主机运行时不做 TCP 探测（`registry.consul.tcp_check_interval` 为 `0s`），原因见 [system.md](architecture/system.md#53-服务注册与发现)。

account 的主要配置段（默认值见 [config.yaml](../app/account/configs/config.yaml)）：

| 配置段 | 内容 | 生效方式 |
|---|---|---|
| `auth` | 公钥、签发方、受众（各服务相同） | 重启 |
| `token` | 私钥、`kid`、访问令牌 900 秒、刷新令牌 30 天、轮换宽限期 10 秒、吊销记录保留 1 小时（不得小于访问令牌有效期） | 重启 |
| `sms` | 发送方（目前只有 `log`）、固定验证码 | 重启 |
| `security` | bcrypt cost（10～14，默认 12）、可信代理 `trusted_proxies`（`${TRUSTED_PROXY:}`，宿主机运行为空，容器模式为网关 IP） | 重启 |
| `cache` | 资料缓存 1800 秒、空值标记 60 秒 | 重启 |
| `kafka`、`outbox` | Kafka 地址；Outbox 轮询间隔 0.5 秒（事务提交后会立即唤醒，轮询只是兜底）、每批 200 条、已发布事件保留 7 天 | 重启 |
| `policy` | 短信：有效期 300 秒、验证 5 次、发送间隔 60 秒、每号每日 10 条、每 IP 每小时 20 次；登录：15 分钟内错 5 次锁定 15 分钟、每 IP 每分钟 20 次 | **热更新**：保存配置文件后自动生效，日志出现 `config reloaded key=policy`；值不合法时保留旧值并记录 ERROR |

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
| `social`、`account` | 地址、调用超时 1 秒 | 重启 |
| `policy` | 每人每 10 秒最多发 30 条消息（所有设备、所有会话合计）、`GetDifference` 一次最多 200 条、缺口超过 1000 条返回 `TOO_LONG` | **热更新** |

access 的主要配置段（默认值见 [config.yaml](../app/access/configs/config.yaml)）：

| 配置段 | 内容 | 生效方式 |
|---|---|---|
| `auth` | 同 account | 重启 |
| `advertise_addr` | 写入在线路由的本实例 gRPC 地址，默认留空，取注册的 gRPC 端点 | 重启 |
| `message` | 地址、调用超时 3 秒 | 重启 |
| `websocket` | 认证超时 10 秒、心跳间隔 30 秒、心跳超时 90 秒（必须大于心跳间隔，同时是路由 TTL）、发送队列 256、写超时 10 秒、最大帧 64 KiB；`drain_window` 停机时分批关闭连接的窗口 5 秒（0～10 秒，0 表示一次全部关闭；进程停机超时固定为 15 秒） | 重启 |
| `policy` | 每个连接每秒最多 30 个上行帧 | **热更新** |

delivery 的主要配置段（默认值见 [config.yaml](../app/delivery/configs/config.yaml)）：

| 配置段 | 内容 | 生效方式 |
|---|---|---|
| `kafka` | Kafka 地址；`session_timeout` 同 social | 重启 |
| `social` | 地址、调用超时 1 秒 | 重启 |
| `push` | 调用 access `Push`、`Kick` 的超时 2 秒 | 重启 |
| `policy` | `push_cooldown`：access 实例不可达（`Unavailable` 或超时）后的冷却期 5 秒（0～60 秒），期间发往它的推送直接跳过，日志出现 `access instance unreachable, connection evicted`；0 表示只摘除连接、下一次立即重新拨号 | **热更新** |

阿里云短信发送方尚未实现（没有 AccessKey、签名和模板），生产环境不能使用 `log` 发送方。

## 开发命令

| 命令 | 作用 |
|---|---|
| `make gen` | 重新生成配置 proto（`make conf`）、接口 proto（`make api`）和依赖注入代码（`make wire`） |
| `make lint` | `buf lint` |
| `make vet` / `make test` | `go vet ./...` / `go test ./...` |
| `make build` | 构建全部服务到 `bin/`，版本号取 `git describe` |
| `make e2e` | 端到端测试，需要依赖组件已启动、account、social、message 已迁移，五个服务都在运行；会加载 `.env`，并清理本机 IP（127.0.0.1）的限流计数。其中 `bloom_test.go` 直接连 Redis，用随机键前缀测试布隆过滤器，结束后清理；`kafka_test.go` 使用专用 topic `marschat.e2e.events`、`marschat.e2e.retry` 和随机消费组，并在死信队列中查找本次写入的记录 |
| `make images` | 宿主机交叉编译全部服务并构建镜像 `marschat/<服务>:dev`，见“容器模式” |
| `make certs` | 生成网关的自签证书到 `deploy/apisix/certs/`（已有时不覆盖） |
| `make cluster-up` / `make cluster-down` | 启动容器模式的全部服务和网关 / 删除这些容器（依赖组件保持运行） |
| `make cluster-test` | 集群测试与故障实验，需要容器模式已启动；客户端只经网关访问，清理网关来源 IP 的限流计数。故障实验会停止、暂停、重启 MarsChat 的容器，见“容器模式” |
| `make keys` | 生成 JWT 密钥对写入 `.env` |
| `make migrate-<服务>` | 数据库迁移，`ARGS` 传子命令 |
| `make grant-admin PHONE=…` | 设置管理员 |

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
