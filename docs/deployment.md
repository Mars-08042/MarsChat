# 部署与运行

> 本文描述**新后端**的运行方式，随重构步骤更新，最近一次更新于步骤 2（2026-11-18）。当前只有本地开发环境；服务容器化与多实例部署在步骤 5 加入。旧单体的部署流程见提交 `6624cea` 中的本文。

## 运行依赖

| 工具 | 版本 | 用途 |
|---|---|---|
| Go | 1.27.3（`go.mod`） | 构建与运行服务 |
| Docker Desktop | 本机 29.9.2，Compose v5.6.1 | 运行依赖组件 |
| buf | 1.71.0 | `make lint`、`make conf`、`make api`；protoc 插件由 `go run` 按固定版本下载，不需要单独安装 |
| make + Git Bash | GNU make 3.81 及以上 | 开发命令；Makefile 使用 `sh`，Windows 上需要 Git Bash |

## 依赖组件

[compose.yaml](../compose.yaml) 只启动依赖组件，项目名 `marschat`，数据保存在命名卷中。宿主机端口为容器默认端口 + 10000，只绑定 `127.0.0.1`。

| 组件 | 镜像 | 宿主机地址 | 说明 |
|---|---|---|---|
| MySQL | `mysql:9.7.4` | `127.0.0.1:13306` | 首次初始化时为每个服务建库建账号，见 [data-model.md](architecture/data-model.md#1-数据库与账号) |
| Redis | `redis:8.8.4` | `127.0.0.1:16379` | 开启 AOF 持久化和密码；加载镜像自带的 RedisBloom 模块（用户 ID 布隆过滤器） |
| Kafka | `apache/kafka:4.4.0` | `127.0.0.1:19092` | 单节点 KRaft；容器网络内地址为 `kafka:29092`；关闭自动建 topic，topic 随第一个使用它的步骤显式创建 |
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
make up      # docker compose up -d --wait，等待全部健康检查通过
make ps
```

Consul UI：<http://127.0.0.1:18500>。

## 数据库迁移

迁移文件在 `app/<服务>/migrations/`（golang-migrate 格式，`<版本>_<名称>.up.sql` / `.down.sql`），由 compose 中的一次性任务执行，服务启动时不自动迁移：多实例同时启动会竞争，生产上也不应由业务进程改表结构。

```bash
make migrate-account                    # 执行全部未执行的迁移（up）
make migrate-social
make migrate-account ARGS="down 1"      # 回滚一步
make migrate-account ARGS="version"     # 查看当前版本
```

迁移记录在各库的 `schema_migrations` 表。首次运行会拉取 `migrate/migrate` 镜像；迁移目录通过 bind mount 挂载，Docker Desktop 需要共享项目根目录（见“首次配置”）。

## 运行服务

每个服务一个终端：

```bash
make run-account   # HTTP 127.0.0.1:18010，gRPC 127.0.0.1:19010
make run-social    # HTTP 127.0.0.1:18020，gRPC 127.0.0.1:19020
```

`make run-<服务>` 会加载 `.env`，再执行 `go run ./app/<服务>/cmd/<服务> -conf app/<服务>/configs`。服务启动时连接 MySQL、Redis 并注册到 Consul，任一依赖不可用会直接退出。首次启动前要先执行对应的 `make migrate-<服务>`。

social 经 Consul 发现并调用 account（申请好友时确认对方存在、列表补全昵称和头像）。account 未启动时 social 照常启动和运行：申请好友返回 `503 DEPENDENCY_UNAVAILABLE`，列表接口返回不带资料的数据并记 WARN。

在终端按 Ctrl+C 停止服务。按 Kratos 的停机流程，服务会先从 Consul 注销再退出（未在 Windows 终端中实测）。进程被强制结束时来不及注销，Consul 中的实例在 20 秒内变为 critical、不再被发现，10 分钟后自动删除。

检查服务状态：

```bash
curl -s http://127.0.0.1:18010/healthz   # 存活探针
curl -s http://127.0.0.1:18010/readyz    # 就绪探针，检查 MySQL 和 Redis
make e2e                                 # 服务发现、account 与 social 全流程，见“开发命令”
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

## 服务配置

配置文件在 `app/<服务>/configs/config.yaml`，结构定义在各服务的 `internal/conf/conf.proto`，约定见 [system.md](architecture/system.md#51-配置)。

- `${XXX}` 引用环境变量 `MARSCHAT_XXX`，`${XXX:默认值}` 在变量缺失时取默认值。默认值对应本地开发环境，一般只需要在 `.env` 中设置密码。
- 时长只能用秒为单位书写，例如 `0.5s`、`3600s`。

可以覆盖的环境变量：

| 变量 | 默认值 | 说明 |
|---|---|---|
| `MARSCHAT_ACCOUNT_HTTP_ADDR` / `MARSCHAT_ACCOUNT_GRPC_ADDR` | `127.0.0.1:18010` / `127.0.0.1:19010` | account 监听地址 |
| `MARSCHAT_SOCIAL_HTTP_ADDR` / `MARSCHAT_SOCIAL_GRPC_ADDR` | `127.0.0.1:18020` / `127.0.0.1:19020` | social 监听地址 |
| `MARSCHAT_MYSQL_ADDR` | `127.0.0.1:13306` | MySQL 地址 |
| `MARSCHAT_REDIS_ADDR` | `127.0.0.1:16379` | Redis 地址 |
| `MARSCHAT_CONSUL_ADDR` | `127.0.0.1:18500` | Consul 地址，`make e2e` 也读取它 |
| `MARSCHAT_LOG_LEVEL` | `info` | `debug`、`info`、`warn`、`error` |
| `MARSCHAT_LOG_FORMAT` | `text` | `text` 或 `json` |
| `MARSCHAT_JWT_KEY_ID` | `dev-1` | 访问令牌头部的 `kid`，为轮换密钥预留 |
| `MARSCHAT_SMS_FIXED_CODE` | `000000` | 开发与 e2e 用的固定验证码，只允许和 `log` 发送方一起使用；置空则随机生成 6 位并写入日志 |
| `MARSCHAT_ACCOUNT_ENDPOINT` | `discovery:///marschat-account` | social 调用 account 的 gRPC 地址；也可以写 `127.0.0.1:19010` 这样的直连地址 |

本地开发时 `registry.consul.health_check` 为 `false`，原因见 [system.md](architecture/system.md#53-服务注册与发现)。

account 的主要配置段（默认值见 [config.yaml](../app/account/configs/config.yaml)）：

| 配置段 | 内容 | 生效方式 |
|---|---|---|
| `auth` | 公钥、签发方、受众（各服务相同） | 重启 |
| `token` | 私钥、`kid`、访问令牌 900 秒、刷新令牌 30 天、轮换宽限期 10 秒、吊销记录保留 1 小时（不得小于访问令牌有效期） | 重启 |
| `sms` | 发送方（目前只有 `log`）、固定验证码 | 重启 |
| `security` | bcrypt cost（10～14，默认 12）、可信代理 `trusted_proxies`（默认空） | 重启 |
| `cache` | 资料缓存 1800 秒、空值标记 60 秒 | 重启 |
| `policy` | 短信：有效期 300 秒、验证 5 次、发送间隔 60 秒、每号每日 10 条、每 IP 每小时 20 次；登录：15 分钟内错 5 次锁定 15 分钟、每 IP 每分钟 20 次 | **热更新**：保存配置文件后自动生效，日志出现 `config reloaded key=policy`；值不合法时保留旧值并记录 ERROR |

social 的主要配置段（默认值见 [config.yaml](../app/social/configs/config.yaml)）：

| 配置段 | 内容 | 生效方式 |
|---|---|---|
| `auth` | 同 account | 重启 |
| `account` | account 地址、调用超时 1 秒 | 重启 |
| `cache` | 群资料 1800 秒、成员列表 1800 秒、空值标记 60 秒 | 重启 |
| `policy` | 每人每小时 20 条好友申请、每人每天建 10 个群、每人每小时 20 次入群、群成员上限 500、建群初始成员上限 50 | **热更新**，同 account；调低群成员上限不会移除已有成员 |

阿里云短信发送方尚未实现（没有 AccessKey、签名和模板），生产环境不能使用 `log` 发送方。

## 开发命令

| 命令 | 作用 |
|---|---|
| `make gen` | 重新生成配置 proto（`make conf`）、接口 proto（`make api`）和依赖注入代码（`make wire`） |
| `make lint` | `buf lint` |
| `make vet` / `make test` | `go vet ./...` / `go test ./...` |
| `make build` | 构建全部服务到 `bin/`，版本号取 `git describe` |
| `make e2e` | 端到端测试，需要依赖组件已启动、account 与 social 已迁移并都在运行；会加载 `.env`，并清理本机 IP（127.0.0.1）的限流计数。其中 `bloom_test.go` 直接连 Redis，用随机键前缀测试布隆过滤器，结束后清理 |
| `make keys` | 生成 JWT 密钥对写入 `.env` |
| `make migrate-<服务>` | 数据库迁移，`ARGS` 传子命令 |
| `make grant-admin PHONE=…` | 设置管理员 |

CI（`.github/workflows/ci.yml`）在推送到 `main`、`dev` 和提交 PR 时执行：`buf lint`、`buf breaking`（PR 与基准分支比较，推送与上一次推送比较）、重新生成代码并检查与提交一致、`go mod tidy -diff`、`go vet`、`go build`、`go test -race`。e2e 需要 compose 环境，暂不在 CI 中运行。

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
