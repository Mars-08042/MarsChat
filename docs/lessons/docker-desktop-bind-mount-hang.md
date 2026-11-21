# Docker Desktop 挂载宿主机目录时 compose 一直卡住

- 涉及：[compose.yaml](../../compose.yaml) 中 MySQL 的 `./deploy/mysql/init:/docker-entrypoint-initdb.d:ro`
- 发现时间：2026-11-18，重构步骤 1 启动依赖组件时

## 现象

`docker compose up -d` 不报错也不退出。Redis、Kafka、Consul 都能单独启动，`docker compose create mysql` 卡住。用 `docker run --rm -v <宿主机目录>:/x redis:8.8.4 ls /x` 测试，无论 D 盘还是 C 盘的目录都卡住；只挂命名卷则立即返回。

## 根因

本机 Docker Desktop 使用 Hyper-V 后端（设置中 `WslEngineEnabled = false`）。这个模式下容器只能挂载 Settings → Resources → File sharing 白名单中的路径。路径不在白名单时，Docker Desktop 会弹窗询问是否共享，CLI 在用户确认前一直等待，看起来就像卡死。白名单里只有其他项目逐个批准过的文件，没有 MarsChat 的目录。

## 解决

在 Docker Desktop 中授权共享该目录（弹窗中确认，或在 File sharing 设置中加入项目根目录）后，等待中的 `docker compose up -d mysql` 继续执行，MySQL 正常完成初始化。

## 预防

- 新机器或新项目先把项目根目录加入 File sharing，后续 APISIX、Prometheus 等配置挂载都依赖它；已写入 [deployment.md](../deployment.md#首次配置)。
- compose 卡住时先用一条只挂宿主机目录的 `docker run` 判断是否是文件共享问题，并查看 Docker Desktop 是否有待确认的弹窗。
- WSL 2 后端没有这个限制。
