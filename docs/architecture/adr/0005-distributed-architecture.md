# ADR-0005 分布式微服务目标架构与技术选型

- 状态：已采纳（2026-11-18 开发者确认）；决策 6 中的文件存储已被 [ADR-0007](0007-object-storage.md) 替代（2026-11-20）
- 日期：2026-11-18
- 关联：[后端重构计划](../../plans/active/backend-refactor.md)、[目标架构图](../../images/proposed-distributed-architecture.svg)

## 背景

旧基线是 Gin 单体加 Vue 前端：在线连接表在进程内存，只能单实例运行（TD-07）；后端没有认证（TD-01）；kafka 模式只是把进程内 channel 换成 Kafka，多实例时会丢投；消息没有序号、确认和补拉。

项目用于求职展示，需要体现可靠消息、多端同步、跨节点投递、水平扩展，以及常见的生产环境实践。旧代码规模小、问题集中在连接管理和消息模型，因此决定在 `dev` 分支重写，不在旧代码上修补。

## 候选方案

| 方案 | 说明 | 结论 |
|---|---|---|
| A 在旧单体上修补 | 修复技术债，再补在线路由 | 问题集中在连接管理和消息模型，修补成本接近重写，也难以体现服务拆分 |
| B 模块化单体 | 单进程内按模块划分 | 简单，但无法演示跨节点投递、独立扩容和故障隔离 |
| C Kratos v3 + Consul + APISIX + Kafka 微服务 | 契约优先，HTTP 与 gRPC 共用一份 proto | **采用** |
| D go-zero + etcd 微服务 | teamgram 的选择，生态成熟 | 开发者指定 Kratos；参考 teamgram 的做法即可，不需要同一框架 |

入口网关另比较了 Kong：Kong 开源版自 3.10 起不再发布预构建镜像，且没有原生 Consul 发现，因此选 APISIX。

## 决策

1. **服务拆分**：access（只承载 WebSocket）、account、social、message、delivery、media。每个服务独立进程、独立数据库。
2. **框架与契约**：Kratos v3.0.0。接口用 protobuf 定义，由 buf 生成 gRPC、HTTP、errors 和 OpenAPI 代码，同一份 proto 同时提供 HTTP 与 gRPC。
3. **入口**：APISIX Standalone。HTTP 按路径直达业务服务，WebSocket 转发到 access。
4. **服务发现与推送寻址**：服务注册发现用 Consul；delivery 推送时按 Redis 在线路由直连目标 access 实例，不经负载均衡。
5. **消息**：Kafka 按会话分区；写库与发事件用事务 Outbox；消费至少一次加幂等；失败进重试队列，多次失败进死信队列。
6. **存储**：MySQL 按服务分库；Redis 用于在线路由、缓存和限流；MinIO 存文件。（2026-11-20 已被 ADR-0007 替代：MinIO 社区版已归档、镜像已删除，改为可切换的本地目录与阿里云 OSS。）
7. **鉴权**：JWT 访问令牌加按设备签发的刷新令牌，Redis 吊销名单支持退出和封禁即时失效。
8. **可观测**：OpenTelemetry + Jaeger；Prometheus + Grafana + Alertmanager。
9. **仓库**：单仓库、单 Go module `github.com/Mars-08042/MarsChat`。
10. **横切约定**（替代 ADR-0001）：protobuf 契约；业务错误用 Kratos errors 表达；日志用 `slog`；表结构用版本化 SQL 迁移管理；文件以文件 ID 引用。细则在重构步骤 1 完成后写入 `system.md`。

## 原因

- **拆分边界按数据归属和扩展特点划分**：access 有状态、连接密集，需要独立扩容，也不能随业务发布重启；message、delivery 是消息核心链路，需要多实例；account、social、media 是读多写少的业务服务。
- **access 不聚合 HTTP**：这样业务服务发布时不会断开长连接。teamgram 把连接层（gnetway、session）与业务层（bff）分开，也是同样的考虑。
- **用事务 Outbox，不直接投 Kafka**：保证写库与发事件一致。teamgram 写库和投递 Kafka 不在同一事务中，这正是我们要避免的。
- **Kratos v3**：当前稳定版，契约优先；HTTP 与 gRPC 共用一份 proto，接口文档可以直接生成。

## 代价与风险

- 组件多，本机内存占用未实测，必要时按重构步骤分组启动。（2026-11-19 实测：Docker Desktop 虚拟机 3.8 GiB；步骤 5 全部容器约 1.3 GiB，Kafka 堆限制为 512 MiB。2026-11-21 步骤 8 实测：加上监控组件后全部容器空闲约 1.6 GiB，其中 Prometheus、Alertmanager、Grafana、Jaeger 合计约 390～510 MiB；1000 条连接压测时各容器峰值之和约 2.8 GiB（含 k6），仍在虚拟机范围内。监控组件放在单独的 profile，日常开发不启动。）
- 跨服务调用增加了延迟和故障点，需要超时、重试和熔断。（2026-11-20 重构步骤 5 已实现，并在容器模式下做了故障实验：杀掉 access、delivery、持有 Outbox 租约的 message，滚动重启 access，暂停 Kafka，停掉 Redis，消息都没有丢失和重复。实验中发现 Redis 停机时 go-redis 的拨号重试会拖垮请求，已修复。见[重构计划的故障实验结果](../../plans/active/backend-refactor.md#故障实验结果)。）
- 跨服务数据只能做到最终一致。
- 单机压测的上限在共用的 MySQL：1000 条连接、约每秒 1000 条消息时推送 P99 约 300 ms，MySQL 约占 2 个 CPU；各服务共用一个实例，连接池上限之和必须小于 MySQL 最大连接数（2026-11-21 步骤 8 压测，见[重构计划的压测结果](../../plans/active/backend-refactor.md#压测结果)）。
- Kratos v3 发布时间较短，Consul 注册、JWT 等 contrib 组件需要在步骤 1 验证可用性。
- APISIX Standalone 与 Consul 发现能否组合使用，官方文档未见示例，需要实测。（2026-11-19 重构步骤 5 实测可用：APISIX 3.20.0 的 `data_plane` + YAML 配置加 `discovery.consul`，两个实例分流，杀掉一个后约 5 秒内摘除，见 [gateway-multi-instance.md](../../design/gateway-multi-instance.md) 第 9 节。）

## 适用条件（何时需要重新评估）

- Kratos v3 的 contrib 组件不可用或不稳定：评估改用 v2.9.x。
- APISIX Standalone 无法配合 Consul：改用 DNS 发现或 Compose 内置 DNS。
- 单机资源无法承载全栈：精简可观测组件，或分组运行。
- 需要支持超大群或多人音视频：另行设计。
