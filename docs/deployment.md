# 部署与运行

> 本文描述**新后端**的运行方式，随重构步骤更新，最近一次更新于步骤 1（2026-11-18）。当前只有本地开发环境；服务容器化与多实例部署在步骤 5 加入。旧单体的部署流程见提交 `6624cea` 中的本文。

## 运行依赖

| 工具 | 版本 | 用途 |
|---|---|---|
| Go | 1.27.3（`go.mod`） | 构建与运行服务 |
| Docker Desktop | 本机 29.9.2，Compose v5.6.1 | 运行依赖组件 |
| buf | 1.71.0 | `make lint`、`make conf`；protoc 插件由 `go run` 按固定版本下载，不需要单独安装 |
| make + Git Bash | GNU make 3.81 及以上 | 开发命令；Makefile 使用 `sh`，Windows 上需要 Git Bash |

## 依赖组件

[compose.yaml](../compose.yaml) 只启动依赖组件，项目名 `marschat`，数据保存在命名卷中。宿主机端口为容器默认端口 + 10000，只绑定 `127.0.0.1`。

| 组件 | 镜像 | 宿主机地址 | 说明 |
|---|---|---|---|
| MySQL | `mysql:9.7.4` | `127.0.0.1:13306` | 首次初始化时为每个服务建库建账号，见 [data-model.md](architecture/data-model.md#1-数据库与账号) |
| Redis | `redis:8.8.4` | `127.0.0.1:16379` | 开启 AOF 持久化和密码 |
| Kafka | `apache/kafka:4.4.0` | `127.0.0.1:19092` | 单节点 KRaft；容器网络内地址为 `kafka:29092`；关闭自动建 topic，topic 随第一个使用它的步骤显式创建 |
| Consul | `hashicorp/consul:2.1.0` | `127.0.0.1:18500` | 单节点 server，带 Web UI |

后续步骤引入的组件和全部应用端口见 [重构计划的端口表](plans/active/backend-refactor.md#端口)。

## 首次配置

在项目根目录（Git Bash）执行：

```bash
cp .env.example .env
```

把 `.env` 中的 `change-me` 换成随机密码，只用字母和数字，避免在 SQL 和 DSN 中转义。`.env` 被 Git 忽略，同时供 Compose 和本机运行的服务使用。

| 变量 | 用途 |
|---|---|
| `MARSCHAT_MYSQL_ROOT_PASSWORD` | MySQL root 密码 |
| `MARSCHAT_ACCOUNT_DB_PASSWORD`、`MARSCHAT_SOCIAL_DB_PASSWORD`、`MARSCHAT_MESSAGE_DB_PASSWORD`、`MARSCHAT_MEDIA_DB_PASSWORD` | 各服务数据库账号的密码 |
| `MARSCHAT_REDIS_PASSWORD` | Redis 密码 |

MySQL 的建库脚本和 root 密码只在数据卷**首次初始化**时生效，之后修改 `.env` 不会改变已有账号的密码。需要重置时见“停止与重置”。

**Docker Desktop 文件共享**：本机 Docker Desktop 使用 Hyper-V 后端，挂载宿主机目录前需要在 Settings → Resources → File sharing 中加入该目录，否则 Docker Desktop 会弹窗询问，确认前 `docker compose up` 会一直卡住。建议直接加入项目根目录，后续 APISIX、Prometheus 的配置挂载也会用到。WSL 2 后端不需要这一步。排查过程见 [lessons/docker-desktop-bind-mount-hang.md](lessons/docker-desktop-bind-mount-hang.md)。

## 启动依赖组件

```bash
make up      # docker compose up -d --wait，等待全部健康检查通过
make ps
```

Consul UI：<http://127.0.0.1:18500>。

## 运行服务

每个服务一个终端：

```bash
make run-account   # HTTP 127.0.0.1:18010，gRPC 127.0.0.1:19010
make run-social    # HTTP 127.0.0.1:18020，gRPC 127.0.0.1:19020
```

`make run-<服务>` 会加载 `.env`，再执行 `go run ./app/<服务>/cmd/<服务> -conf app/<服务>/configs`。服务启动时连接 MySQL、Redis 并注册到 Consul，任一依赖不可用会直接退出。

在终端按 Ctrl+C 停止服务。按 Kratos 的停机流程，服务会先从 Consul 注销再退出（未在 Windows 终端中实测）。进程被强制结束时来不及注销，Consul 中的实例在 20 秒内变为 critical、不再被发现，10 分钟后自动删除。

检查服务状态：

```bash
curl -s http://127.0.0.1:18010/healthz   # 存活探针
curl -s http://127.0.0.1:18010/readyz    # 就绪探针，检查 MySQL 和 Redis
make e2e                                 # 通过 Consul 发现两个服务并调用健康检查
```

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

本地开发时 `registry.consul.health_check` 为 `false`，原因见 [system.md](architecture/system.md#53-服务注册与发现)。

## 开发命令

| 命令 | 作用 |
|---|---|
| `make gen` | 重新生成配置 proto 代码（`make conf`）和依赖注入代码（`make wire`） |
| `make lint` | `buf lint` |
| `make vet` / `make test` | `go vet ./...` / `go test ./...` |
| `make build` | 构建全部服务到 `bin/`，版本号取 `git describe` |
| `make e2e` | 端到端测试，需要依赖组件和服务都已启动 |

CI（`.github/workflows/ci.yml`）在推送到 `main`、`dev` 和提交 PR 时执行：`buf lint`、重新生成代码并检查与提交一致、`go mod tidy -diff`、`go vet`、`go build`、`go test -race`。

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
