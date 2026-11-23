# 设计：可观测与压测（重构步骤 8）

- 状态：已采纳 | 日期：2026-11-21（2026-11-21 开发者确认第 9 节全部问题按推荐方案实施；实施中的调整见第 10 节） | 关联：[重构计划步骤 8](../plans/completed/backend-refactor.md#8-可观测与压测)、[ADR-0005](../architecture/adr/0005-distributed-architecture.md)、[网关与多实例设计](gateway-multi-instance.md)、[消息链路设计](message-pipeline.md)、[通话信令设计](call-signaling.md)

## 1. 背景与目标

重构计划对步骤 8 的要求：

- 部署 Prometheus、Grafana、Alertmanager。指标包括连接数、消息端到端延迟、推送失败数、Kafka 积压、缓存命中率。
- 接入 OpenTelemetry 和 Jaeger，消息 ID 贯穿日志与 Trace；开启 MySQL 慢查询日志。
- 用 k6（Docker 镜像运行）压测 WebSocket 收发和热点读接口，记录在线连接数、P99 延迟和缓存防护效果。

ADR-0005 已确定选型：OpenTelemetry + Jaeger；Prometheus + Grafana + Alertmanager。

现状（2026-11-21 核实）：

- 服务只有日志，没有指标和 Trace。
  - 访问日志记录每个请求的耗时；GORM 超过 200 毫秒的 SQL 记 WARN；
  - 故障实验的数据靠测试代码自己计时。
- Kratos v3.0.0 把 OpenTelemetry 相关能力移出了核心模块，放在 `github.com/go-kratos/kratos/contrib/otel/v3`（本地源码 `tmp/kratos/contrib/otel`），分为三个子包：
  - `tracing`：服务端和客户端中间件，gRPC 元数据与 HTTP 头按 W3C `traceparent` 传播；
  - `metrics`：服务端和客户端的请求计数与耗时直方图，基于 OTel metric API；
  - `log`：slog 桥接。
- Outbox 表（account、social、message 各一张）没有记录 Trace 上下文的列。Kafka 记录头目前有事件类型、事件 ID、来源，以及重试、死信相关的头。
- delivery 只有 gRPC 服务器，没有 HTTP 端口。
- Docker Desktop 虚拟机共 3.8 GiB。当前占用约 1.75 GiB：MarsChat 约 1.35 GiB（Kafka 515 MiB、MySQL 406 MiB），其余是本机其他项目的容器。

目标：

- 每个服务暴露 `/metrics`，Prometheus 经 Consul 发现所有实例，多副本自动纳入。
- 一条消息从 WebSocket 帧开始，经 message、Kafka、delivery，到推送进入接收方连接所在的 access，在 Jaeger 中是一条完整的 Trace；日志带 `trace_id`，Trace 带消息 ID、会话 ID、seq。
- Grafana 预置数据源和一个总览面板；Alertmanager 收到预置规则产生的告警。
- 压测给出可复现的数据：在线连接数、发送确认与推送的 P99、热点读接口的 P99 与缓存命中率，以及击穿、穿透防护前后的对比；结果写入重构计划。

不在本步骤范围内：日志集中收集（Loki 等，资源不够，日志仍在容器标准输出）；数据库调用的 Span（GORM 的 OTel 插件，需要再加依赖，慢 SQL 已有日志和 MySQL 慢查询日志）；告警的外部通知（第 9 节问题 5）。

## 2. 需求与约束

- **资源**：监控组件常驻内存目标合计不超过 600 MiB，各自设 `mem_limit`，实施时实测。k6 只在压测时运行。
- **两种运行方式**：
  - 监控组件只在容器模式下工作；
  - 宿主机运行的服务监听 `127.0.0.1`，容器里的 Prometheus 访问不到，这与 Consul 不做 TCP 探测的原因相同；
  - 宿主机运行时 `/metrics` 照常可以用 curl 查看，Trace 可选发往宿主机映射的 Jaeger 端口。
- **不影响现有功能和测试**：
  - 不配置 OTLP 地址时不导出 Trace，`/metrics` 照常可用；
  - 单元测试和 e2e 不依赖监控组件。
- **安全**：
  - `/metrics` 挂在各服务的 HTTP 端口上，网关只转发明确列出的 `/v1/...` 和 `/ws` 路径，外部访问不到；
  - 监控组件的宿主机端口只绑定 `127.0.0.1`；
  - Trace 和指标的标签中不放令牌、邮箱、消息正文和 IP；
  - 用户 ID 只出现在 Span 属性中，不作为指标标签，避免基数爆炸。
- **端口**：沿用计划中的端口表。

  | 组件 | 宿主机端口 |
  |---|---|
  | Prometheus | 19090 |
  | Alertmanager | 19093 |
  | Grafana | 13000 |
  | Jaeger UI / OTLP gRPC / OTLP HTTP | 26686 / 14317 / 14318 |

  delivery 新增 HTTP 18040（问题 3）。

## 3. 方案

### 3.1 总体拓扑

```
各服务（容器模式）──/metrics──→ Prometheus ──告警──→ Alertmanager
   │  ↑ Consul 发现实例              ↑
   │                              Grafana（数据源：Prometheus、Jaeger）
   └──OTLP gRPC（Trace）──→ Jaeger（内存存储）
k6（压测时）──→ APISIX 网关 ──→ 各服务
```

- 监控组件放在 Compose profile `monitoring`，`make monitoring-up` 启动，日常开发不启动。k6 放在 profile `loadtest`，用 `docker compose run --rm` 运行。
- 镜像锁定版本：
  - `prom/prometheus:v3.16.0`；
  - `prom/alertmanager:v0.35.0`；
  - `grafana/grafana:13.3.1`；
  - `jaegertracing/jaeger:2.23.0`；
  - `grafana/k6:2.4.0`。

  以上均为 2026-11-21 在 Docker Hub 上查到的最新稳定版。
- 配置文件放在 `deploy/prometheus/`、`deploy/alertmanager/`、`deploy/grafana/`、`deploy/jaeger/`，以只读方式挂载。Prometheus 数据放命名卷 `marschat_prometheus-data`，保留 3 天、上限 1 GiB。Jaeger 与 Grafana 不需要持久化：Jaeger 用内存存储，Grafana 的面板来自配置文件。

### 3.2 指标

**导出方式**：

- 服务内统一用 OTel metric API 记录，与 Kratos 的 `metrics` 中间件一致。
- 经 `go.opentelemetry.io/otel/exporters/prometheus` 注册到 Prometheus 默认注册表，用 `promhttp` 在 HTTP 服务器上提供 `/metrics`。
- 默认注册表自带 Go 运行时和进程指标（堆内存、goroutine、GC、文件描述符）。

公共初始化放在 `pkg/telemetry`：

- 创建 MeterProvider 和 TracerProvider，并设为全局；
- 指定耗时直方图的桶；
- 提供 `/metrics` 处理函数；
- 停机时刷新并关闭。

**发现**：

- Prometheus 用 `consul_sd_configs` 发现 `marschat-*` 服务，只保留检查通过的实例。
- `__address__` 取 Consul 中登记的 `Address:Port`，按现有约定就是 HTTP 端点。
- 服务名作为 `service` 标签。
- 抓取间隔 5 秒：压测时需要细粒度，数据只保留 3 天。

**指标**（OTel 名称导出为 Prometheus 时会加单位后缀和 `_total`，下表写导出后的名字）：

| 来源 | 指标 | 标签 | 用途 |
|---|---|---|---|
| Kratos 中间件（所有服务的 HTTP、gRPC 服务端与客户端） | `server_requests_seconds`、`server_requests_code_total`、`client_requests_seconds`、`client_requests_code_total` | `kind`、`operation`、`code`、`reason` | QPS、错误率、P99；客户端指标反映依赖的耗时与失败 |
| access | `marschat_ws_connections` | — | 在线连接数（每实例），Grafana 中求和 |
| access | `marschat_ws_closes_total` | `code` | 关闭原因：4003 慢连接、4008 帧频率超限、1001 停机等 |
| access | `marschat_ws_frames_total` | `type` | 上行帧按类型计数（发消息、心跳、通话） |
| access | `marschat_call_events_total` | `event` | 通话：邀请、接听、按原因结束 |
| access | `marschat_call_records_total` | `outcome` | 通话记录写入结果：`written`、`failed`（重试用尽或参数错误）、`dropped`（停机时放弃），见 [call-record.md](call-record.md) |
| delivery | `marschat_push_total` | `kind`、`result` | 推送结果：`delivered`、`offline`（没有在线连接）、`failed`、`cooling` |
| delivery | `marschat_message_delivery_seconds` | `conversation_type` | **消息投递延迟**：推送进入 access 时刻减去消息写库时间（`Message.created_at`），覆盖 Outbox、Kafka、delivery 三段 |
| `pkg/kafka` 消费者 | `marschat_kafka_consumed_total`、`marschat_kafka_handle_seconds` | `group`、`topic`、`result` | 处理结果：`ok`、`retry`、`dlq` |
| delivery（kadm） | `marschat_kafka_consumer_lag` | `group`、`topic` | **Kafka 积压**：各消费组在各 topic 上的未消费条数（问题 4） |
| `pkg/outbox` | `marschat_outbox_published_total`、`marschat_outbox_publish_delay_seconds` | `service`、`result` | 发布结果；发布时刻减去事件写入时刻 |
| `pkg/cache` | `marschat_cache_requests_total` | `cache`、`result` | **缓存命中率**：`hit`、`null_hit`（空值标记）、`miss` |
| `pkg/cache` | `marschat_cache_loads_total` | `cache`、`result` | 实际回源次数。与 `miss` 对比，可以看出 singleflight 和重建锁合并了多少回源 |
| `pkg/bloom` | `marschat_bloom_checks_total` | `filter`、`result` | 布隆过滤器拦截了多少不存在的 ID（穿透防护） |
| `pkg/ratelimit` | `marschat_ratelimit_total` | `name`、`result` | 限流放行与拒绝 |

“端到端延迟”的两种口径：

- **服务端**：`marschat_message_delivery_seconds`，从写库到推送入队；
- **客户端**：k6 在消息正文中带发送时刻，接收方收到推送时计算（3.8 节）。

两者之差主要是发送确认之前的 access 到 message 这一段，以及 WebSocket 写出这一段。access 到 message 的耗时由 access 的客户端指标单独反映。

### 3.3 链路追踪

- **SDK 与导出**：OTel Go SDK，经 OTLP gRPC 发往 Jaeger。
  - 采样为 `ParentBased(TraceIDRatioBased(sample_ratio))`，默认 1.0，压测时调低（例如 0.05），避免 Jaeger 内存暴涨；
  - 资源属性包括 `service.name`、`service.version`、`service.instance.id`（与 Consul 中的实例 ID 一致）；
  - `telemetry.otlp_endpoint` 为空时不导出。
- **同步调用**：
  - Kratos `tracing.Server()` 加入 `bootstrap.ServerMiddlewares`，排在访问日志之后、令牌校验之前；
  - `tracing.Client()` 加入所有 gRPC 客户端，包括 `pkg/accessclient` 的直连客户端；
  - 访问日志改为带 `trace_id`。
- **WebSocket**：
  - access 为每个业务帧（`SendMessage`、`CallRequest`）开一个服务端 Span，名称如 `ws.SendMessage`；
  - 调 message 时，经 tracing 客户端中间件传播；
  - 认证帧和心跳不开 Span。
- **跨 Kafka**：
  - `outbox.Add` 从事务的 context（`db.Statement.Context`）取当前 Span 上下文，写入 Outbox 新列 `trace_parent`（W3C `traceparent`，55 个字符，没有 Span 时为空串）；
  - 发布器把它放进 Kafka 记录头 `traceparent`；
  - 重试和死信转发时，记录头原样复制（现有的 `cloneHeaders`）；
  - `pkg/kafka` 消费者取出后，以它为父开消费 Span，名称为 `consume <topic>`，属性包括事件 ID、类型、分区、位点和排队时长；
  - 这样 Jaeger 中一条消息的 Trace 依次是：`ws.SendMessage` → message `SendMessage` →（异步）delivery `consume marschat.message.events` → access `Push`。
  - 这需要三个库各加一个迁移（问题 2）。
- **业务属性**：
  - 发送路径和投递路径的 Span 带 `marschat.message_id`、`marschat.conversation_id`、`marschat.seq`、`marschat.event_id`；
  - 对应的日志行带同样的字段；
  - 用户 ID 只出现在 Span 属性中。
- **Jaeger**：
  - 用 v2 all-in-one 镜像，内存存储；
  - 挂载配置文件，把 `max_traces` 从默认的 100000 调到 20000，再加 `mem_limit`；
  - 重启后 Trace 丢失，这对开发和压测足够。

### 3.4 日志与 Trace 关联

- `bootstrap.NewLogger` 包一层 slog Handler：记录携带的 context 中有 Span 时，追加 `trace_id`、`span_id` 两个属性。实现用 Kratos `tracing.TraceAttrs`。
- 关键路径的日志改用 `InfoContext` / `WarnContext`，才能拿到 context：
  - 访问日志；
  - message 发送；
  - Outbox 发布失败；
  - 消费者的重试、死信；
  - delivery 推送失败；
  - access 发送失败与通话。
- 其他日志不改，不加 `trace_id`。

### 3.5 告警

规则放在 `deploy/prometheus/rules.yml`，Prometheus 评估后发给 Alertmanager。

| 告警 | 条件（持续时间） | 说明 |
|---|---|---|
| `InstanceDown` | `up == 0`（1 分钟） | 实例无法抓取 |
| `HighErrorRate` | 某服务 5xx 占比超过 5%（5 分钟） | 依赖故障或程序错误 |
| `HighLatency` | 某操作服务端 P99 超过 500 毫秒（5 分钟） | 慢接口 |
| `KafkaConsumerLag` | 某消费组积压超过 1000 条（5 分钟） | 消费跟不上或消费者停了 |
| `KafkaDeadLetter` | 10 分钟内有事件进入死信队列 | 需要人工排查（`make dlq`） |
| `PushFailures` | 推送失败率超过 5%（5 分钟） | access 实例不可达等 |
| `OutboxPublishDelay` | 发布延迟 P99 超过 5 秒（5 分钟） | Kafka 不可用或租约问题 |
| `MessageDeliveryLatency` | 投递延迟 P99 超过 1 秒（5 分钟） | 端到端变慢 |

Alertmanager 只配置一个不发送的接收方，告警在它的界面（19093）和 Grafana 中查看（问题 5）。实施时用故障实验触发至少两条告警（杀掉实例、暂停 Kafka），确认链路通畅。

### 3.6 Grafana

- 数据源（Prometheus、Jaeger）和面板都用 provisioning 文件预置，放进仓库。
- 总览面板分区如下：
  - 各服务 QPS、错误率、P99；
  - 在线连接数与关闭原因；
  - 消息投递延迟、推送结果；
  - Kafka 积压与消费结果；
  - Outbox 发布延迟；
  - 缓存命中率与回源次数；
  - Go 内存与 goroutine。
- 认证：
  - 开启匿名只读（Viewer），端口只绑定 `127.0.0.1`；
  - 管理员密码从 `.env` 的 `GRAFANA_ADMIN_PASSWORD` 读取，不写进仓库；
  - `make keys` 增加生成这一项，已有时不覆盖，也不打印（问题 8）。

### 3.7 MySQL 慢查询日志

- Compose 中 MySQL 的启动参数加：
  - `--slow-query-log=ON`；
  - `--long-query-time=0.2`，与 GORM 的慢 SQL 阈值一致；
  - `--log-queries-not-using-indexes=OFF`，避免小表全扫描刷屏。
- 日志文件写在数据卷中，用 `make mysql-slow` 查看最近的记录。
- 改启动参数需要重建 MySQL 容器，数据卷保留（问题 6）。

### 3.8 压测

**工具与运行**：

- k6 2.4.0 镜像（`k6/websockets` 在 1.6.0 起为稳定模块），连在 Compose 网络中，经网关压测：`ws://apisix:9080/ws`、`http://apisix:9080`。
- WebSocket 使用 access 已支持的 JSON 子协议 `marschat.v1.json`，k6 不需要处理 protobuf。
- 一个 VU 可以同时持有多个 WebSocket 连接，用于减少 k6 自身的内存（每个 VU 是一个 JS 运行时）。
- 脚本放在 `test/load/k6/`，结果摘要输出到 `data/load/`（被 Git 忽略），关键数据写入重构计划。

**准备数据**：

- `test/load/seed_test.go`（构建标签 `load`，`make load-seed`）经网关注册 N 个用户、两两加好友、建一个热点群。
- 注册受“每 IP 每小时 20 次发码”限制，沿用集群测试的做法：每批注册后清除本机来源地址的限流计数（`testkit.ResetRateLimits`）。
- 用户与令牌写入 `data/load/users.json`。
- 访问令牌 15 分钟有效，所以准备数据后立即压测。
- 验证码依赖 account 的 `log` 发送方。压测期间按“验证码邮件”一节，在命令的环境中把 account 覆盖为 `log` 模式，结束后恢复，`.env` 不改。

**场景**：

| 场景 | 做法 | 记录 |
|---|---|---|
| WebSocket 收发 | 用户两两配对，连接数分档递增（例如 200、500、1000，以 k6 与 access 的内存为上限）；每个连接每秒发 1 条消息给对方，正文带发送时刻 | 在线连接数；发送确认 P50、P99；推送 P50、P99（接收方收到推送的时刻减去正文中的发送时刻）；错误与断线数；access、message、delivery 的 CPU 与内存；`marschat_message_delivery_seconds` 与 Kafka 积压 |
| 热点读 | 固定到达率读少量热点资源：用户资料（account 缓存）、群资料与成员（social 缓存）、会话列表（message） | 各接口 P99；`marschat_cache_requests_total` 的命中率；MySQL QPS |
| 击穿 | 热点读进行中删除热点群的缓存键 | 删除后的回源次数（`marschat_cache_loads_total`，预期每个实例 1 次，跨实例由重建锁合并）；P99 是否抖动 |
| 穿透 | 用不存在的 ID 读用户资料和群资料 | 布隆过滤器拦截数；MySQL 是否收到查询 |

压测前记录基线（镜像版本、实例数、`mem_limit`、Docker Desktop 资源），结果附原始摘要的路径。

## 4. 接口、配置与数据变化

### 4.1 配置

`pkg/conf/conf.proto` 新增 `Telemetry`，各服务的 `Bootstrap` 加 `telemetry` 字段：

```proto
message Telemetry {
  // OTLP gRPC 地址，例如 jaeger:4317；为空时不导出 Trace
  string otlp_endpoint = 1;
  // Trace 采样比例，0～1
  double sample_ratio = 2;
}
```

```yaml
telemetry:
  otlp_endpoint: ${OTLP_ENDPOINT:}
  sample_ratio: ${TRACE_SAMPLE_RATIO:1}
```

Compose 中的服务设置 `MARSCHAT_OTLP_ENDPOINT=jaeger:4317`。监控 profile 没有启动时，导出失败只在 SDK 内部重试并丢弃，不影响请求；实施时验证不刷错误日志。

### 4.2 数据库迁移（问题 2）

account 000003、social 000002、message 000002：

```sql
ALTER TABLE outbox_events ADD COLUMN trace_parent VARCHAR(64) NOT NULL DEFAULT '' AFTER payload;
```

- 加列带默认值，MySQL 8+ 为即时 DDL（`ALGORITHM=INSTANT`）。
- 旧数据为空串，发布时不加记录头。
- down 迁移删除这一列。

### 4.3 端口与部署（问题 3）

- delivery 新增 HTTP 服务器 18040，提供 `/healthz`、`/readyz`、`/metrics`。按现有约定，HTTP 端点排在第一个，Consul 中登记的 `Address:Port` 改为 HTTP 端点。delivery 不被其他服务调用，不受影响。
- Compose 新增 profile `monitoring`（Prometheus、Alertmanager、Grafana、Jaeger）和 `loadtest`（k6）。
- Makefile 新增：
  - `monitoring-up`、`monitoring-down`；
  - `load-seed`、`load-ws`、`load-read`；
  - `mysql-slow`。

### 4.4 新增依赖（问题 1）

| 模块 | 版本 | 用途 |
|---|---|---|
| `github.com/go-kratos/kratos/contrib/otel/v3` | 与现有 contrib 模块同一提交（`v3.0.0-20261026152050-dd0fb7df1e32`） | tracing、metrics 中间件 |
| `go.opentelemetry.io/otel`、`otel/sdk`、`otel/sdk/metric`、`otel/trace`、`otel/metric` | v1.47.0 | OTel API 与 SDK |
| `go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc` | v1.47.0 | 导出 Trace 到 Jaeger |
| `go.opentelemetry.io/otel/exporters/prometheus` | v0.69.0 | 指标导出到 Prometheus 注册表 |
| `github.com/prometheus/client_golang` | v1.25.0 | `promhttp` 处理函数、默认注册表 |
| `github.com/twmb/franz-go/pkg/kadm` | v1.20.0 | 查询消费组积压（问题 4） |

版本为 2026-11-21 在 Go 模块代理上查到的最新版；`contrib/otel` 要求 otel ≥ v1.43.0。

## 5. 候选方案与权衡

| 问题 | 采用 | 备选与不采用的原因 |
|---|---|---|
| 指标库 | OTel metric API + Prometheus exporter | 直接用 `client_golang`：更直接，但 Kratos v3 的中间件基于 OTel，两套 API 并存更乱 |
| 实例发现 | Prometheus 的 Consul 发现 | Docker DNS 发现（`dns_sd_configs` 按服务名解析出全部副本）：更简单，但拿不到实例 ID 等元数据，也绕开了已有的注册中心 |
| Kafka 积压 | delivery 内用 kadm 定期查询所有 MarsChat 消费组 | `danielqsj/kafka-exporter` v1.11.0：标准做法，但发布说明没有确认支持 Kafka 4.x，而且多一个容器。kadm 与现有的 franz-go 同源，已在 Kafka 4.4 上使用。代价是两个 delivery 实例报告相同的值，面板和告警取 `max` |
| Trace 存储 | Jaeger v2 内存存储 | Grafana Tempo：要额外存储与配置；ADR-0005 已选 Jaeger |
| 跨 Kafka 传播 | Outbox 加列，发布时转成记录头 | 放进 payload：要改所有事件的 proto，消费方也要感知；不传播：异步段断成两条 Trace，只能靠消息 ID 关联 |
| 压测工具 | k6（计划既定） | 用 Go 写压测程序：内存占用更低，能开更多连接，但计划和 ADR-0005 都写了 k6，k6 的报告和阈值也更标准 |
| 告警通知 | 不发送，界面查看 | 邮件（已有 SMTP 配置）：会真实发信，属于对外动作，告警规则还没调稳时会频繁打扰 |

## 6. 新增依赖与环境变化

- Go 依赖见 4.4 节，需要开发者同意后修改 `go.mod`。
- 需要拉取 5 个镜像（Docker Hub 官方仓库，amd64 压缩大小）：

  | 镜像 | 大小 |
  |---|---|
  | `prom/prometheus:v3.16.0` | 116 MB |
  | `prom/alertmanager:v0.35.0` | 44 MB |
  | `grafana/grafana:13.3.1` | 506 MB |
  | `jaegertracing/jaeger:2.23.0` | 57 MB |
  | `grafana/k6:2.4.0` | 36 MB |

- 重建 MySQL 容器以加入慢查询日志参数，数据卷保留。
- `.env` 新增 `GRAFANA_ADMIN_PASSWORD`，由 `make keys` 生成，`.env.example` 加说明。

## 7. 风险与验证

| 风险 | 影响 | 处理 |
|---|---|---|
| 内存不足 | Docker Desktop 3.8 GiB，加上监控和 k6 后可能触发 OOM | 各组件设 `mem_limit`；实施第 1 段实测常驻内存；压测按档位递增，记录 k6 自身内存，以它为连接数上限 |
| Trace 导出拖慢请求 | 批量导出在后台进行，队列满时丢弃 | 使用 SDK 默认的批处理器；压测时把采样比例调低，并对比开关前后的 P99 |
| 指标基数 | 标签中出现用户 ID、连接 ID 会让 Prometheus 内存暴涨 | 只用有限取值的标签（操作名、结果、原因、关闭码）；代码评审时检查 |
| k6 测的是网关之后的系统，单机压测 | 结果反映的是 Docker Desktop 单机的上限，不代表生产 | 记录环境与瓶颈所在（CPU、内存、哪一个服务），作为对比基线，不当作容量结论 |
| 宿主机模式没有监控 | 宿主机运行时只能用 curl 看 `/metrics` | 监控与压测都在容器模式下进行，文档写明 |

验证：

- **单元测试**：
  - `pkg/telemetry` 初始化与关闭；
  - Outbox 写入和发布时 `traceparent` 的往返；
  - 消费者从记录头恢复父 Span（用 SDK 的内存导出器 `tracetest`）；
  - 缓存、推送等指标的计数（用 SDK 的 `ManualReader` 读取）。
- **e2e**：现有用例照常通过；新增一项，检查各服务 `/metrics` 返回 200 且包含 Kratos 的请求指标。
- **集群**：
  - Prometheus 的 targets 中各服务全部 `up`；
  - 发一条消息后，Jaeger 中能按消息 ID 查到一条跨 access、message、delivery 的 Trace；
  - 杀掉实例触发 `InstanceDown`，暂停 Kafka 触发积压告警，各验证一次。
- **压测**：按 3.8 节执行，结果写入计划。
- **检查顺序**：`buf lint` → `make gen` → `go vet`（含 `e2e`、`cluster` 标签）→ `go build` → `go test -race ./...` → `go mod tidy -diff`。

## 8. 实施顺序

1. **监控组件与指标**：
   - `pkg/telemetry`、配置、Kratos `metrics` 中间件、各服务 `/metrics`、delivery 的 HTTP 端口；
   - Compose `monitoring` profile、Prometheus（Consul 发现）、Grafana 数据源；
   - 实测内存。
2. **业务指标**：access、delivery、`pkg/kafka`、`pkg/outbox`、`pkg/cache`、`pkg/bloom`、`pkg/ratelimit` 的指标；kadm 积压；Grafana 总览面板；告警规则与 Alertmanager。
3. **链路追踪**：
   - OTLP 导出、tracing 中间件、WebSocket Span；
   - Outbox 迁移与 `traceparent` 传播、消费者 Span；
   - 日志关联、业务属性；
   - Jaeger 配置。
4. **MySQL 慢查询日志**、集群验证（targets、Trace、两条告警）。
5. **压测**：准备数据、两类场景、击穿与穿透实验，记录结果。
6. **文档**：
   - `system.md`（遥测、中间件链、端口）、`deployment.md`（监控组件、配置、Makefile、慢查询日志）；
   - `data-model.md`（Outbox 新列）、`data-flow.md`（Trace 传播）；
   - 重构计划（压测结果）、ADR-0005 风险一节（实测内存）。

每段完成后按第 7 节的顺序检查，改了 proto 或 wire 后执行 `make gen`。

## 9. 待确认问题

1. **新增依赖与镜像**：推荐同意。
   - Go 依赖见 4.4 节，需要修改 `go.mod`；
   - 拉取 5 个镜像，合计约 760 MB（压缩），其中 Grafana 506 MB 最大；
   - 镜像在第 1 段实施前拉取。
2. **Outbox 加 `trace_parent` 列**：account、social、message 各一个迁移，用于跨 Kafka 传播 Trace。推荐采用。备选：不跨 Kafka 传播，异步段另起 Trace，靠消息 ID 关联，不改数据库。
3. **delivery 新增 HTTP 端口 18040**：提供 `/healthz`、`/readyz`、`/metrics`，Consul 中登记的地址随之改为 HTTP 端点。推荐采用。备选：只为 `/metrics` 单独开一个不注册的监听端口，与其他服务的约定不一致。
4. **Kafka 积压的统计方式**：
   - 推荐由 delivery 用 kadm 统计（新依赖 `franz-go/pkg/kadm`）；
   - 备选：部署 `kafka-exporter` 容器，但它对 Kafka 4.x 的支持未确认，实施时要先验证。
5. **告警不发外部通知**：只在 Alertmanager 和 Grafana 中查看。推荐采用；以后需要时再接邮件或 IM，那属于对外动作，届时单独确认。
6. **MySQL 慢查询日志**：阈值 200 毫秒，需要重建 MySQL 容器（数据卷保留，期间依赖 MySQL 的服务短暂不可用）。推荐采用。
7. **压测方式与规模**：推荐采用。
   - k6 在 Docker 中运行，一个 VU 持有多个连接；
   - 连接数按 200、500、1000 递增，以内存为上限；
   - 准备数据经网关注册，并清除限流计数，压测期间把 account 临时覆盖为 `log` 发送方。
8. **Grafana 认证**：匿名只读，端口只绑定本机；管理员密码由 `make keys` 写入 `.env`，不打印。推荐采用。

## 10. 实施中的调整

2026-11-21 实施时与上文不同或上文没有写明的地方：

**指标与告警**

1. **中间件顺序**：服务端为链路追踪 → 指标 → 访问日志 → 鉴权 → 参数校验。链路追踪放在最外层，访问日志能带上 `trace_id`，被鉴权拒绝的请求也有 Span 和指标。客户端为链路追踪 → 指标 → 重试 → 熔断，指标记录的是包含重试在内的整次调用。直连 access 的客户端（`pkg/accessclient`）只挂前两个。
2. **Prometheus 不按健康状态过滤实例**（3.2 节写的是只保留检查通过的实例）。被杀的实例在 Consul 中保持 critical 600 秒后才注销，只保留 passing 的话它会直接从抓取目标中消失，`up == 0` 永远不出现，`InstanceDown` 无法触发。代价是注销前的 10 分钟内面板上能看到这个实例。
3. **推送指标的语义**：`marschat_push_total` 按对 access 实例的每次调用计数（`ok`、`failed`、`cooling`），接收方全部不在线的事件计一次 `offline`；`marschat_message_delivery_seconds` 在每次推送成功时记录一次，从消息写库到 access 返回。推送失败率的分母包含 `cooling`（冷却期跳过的调用）。
4. **积压告警的验证方式**（7 节写的是暂停 Kafka）：暂停 Kafka 时生产和消费一起停，积压不会增长；停止 delivery 又会让积压指标一起消失（积压由 delivery 统计，见 TD-42）。改为停止两个 message 实例，再用 `test/load/k6/lag_events.js` 产生 2000 条 social 事件，`KafkaConsumerLag` 5 分钟后触发、启动 message 后恢复。另外用 promtool 为全部 8 条规则写了单元测试（`deploy/prometheus/rules_test.yml`，`make rules-test`），覆盖该触发、不该触发和恢复。

**链路追踪与日志**

5. **消息标识**：消息没有 `message_id` 字段，`(conversation_id, seq)` 唯一标识一条消息。Span 属性为 `marschat.conversation_id`、`marschat.seq`，发送路径另带 `marschat.client_msg_id`，消费者 Span 带 `marschat.event_id`、`marschat.queue_ms`。日志不在每一行重复这些字段，靠 `trace_id` 关联到 Span。
6. **生产者 Span 的起点**是事件写入 Outbox 的时刻（`created_at`），覆盖在 Outbox 中等待的时间，长度与 `marschat_outbox_publish_delay_seconds` 一致。
7. **`outbox.Add` 显式接收 context**（3.3 节写的是从 `db.Statement.Context` 取）：调用方都有请求的 context，显式传入比依赖事务对象是否设置了 context 更直接。
8. **日志关联**：`telemetry.LogHandler` 直接从 context 读取 `SpanContext` 实现 slog Handler，没有用 Kratos 的日志工具（它面向 Kratos 自己的日志接口）。只有带 context 的日志方法（`InfoContext` 等）才能拿到 `trace_id`，相关的日志调用一并改了。
9. **Span 中的对端地址**：Kratos tracing 中间件记录 `net.peer.ip`，取值是网关或上游服务的容器地址，不是用户 IP；Jaeger 只绑定本机。
10. **Jaeger 配置精简**：只保留 `jaeger_storage`、`jaeger_query`、`healthcheckv2` 扩展和 OTLP 接收器，关闭 Jaeger 自身的指标，设 `GOMEMLIMIT=200MiB`。

**Grafana**

11. **不配置 Jaeger 数据源**（3.6 节）：Grafana 13 的 Jaeger 数据源调用 Jaeger 旧的 `/api/services`、`/api/traces`，Jaeger 2.23 已经移除这些接口，只保留 `/api/v3`（grafana-jaeger-datasource issue #87）。面板顶部放 Jaeger UI 和 Alertmanager 的链接。
12. **内存与挂载**：Grafana 启动后约 236 MiB，接近最初的 256 MiB 限制，调为 384 MiB 并设 `GOMEMLIMIT=320MiB`。只挂载 `provisioning/datasources`、`provisioning/dashboards` 和面板目录：挂载整个 `provisioning` 目录时，Grafana 会因为缺少 `plugins`、`alerting` 子目录报错。
13. **环境变量名**为 `MARSCHAT_GRAFANA_ADMIN_PASSWORD`，与 `.env` 中其他变量的前缀一致（3.6 节写的是 `GRAFANA_ADMIN_PASSWORD`）。

**依赖与资源**

14. **依赖**：除 4.4 节所列，还直接依赖 `prometheus/client_model` v0.6.3 和 `prometheus/otlptranslator` v1.0.0（导出器的命名策略、测试中读取指标），间接升级 gRPC 1.83.2、protobuf 1.36.12 与 genproto。
15. **实测内存**（2026-11-21）：监控组件空闲时 Prometheus 约 93 MiB、Alertmanager 约 21 MiB、Grafana 约 250 MiB、Jaeger 约 28 MiB，合计约 390 MiB；跑完集群测试与压测后约 510 MiB（Jaeger 随保存的 Trace 增长到约 170 MiB），在 600 MiB 的目标内。应用服务接入遥测后各增加约 15～25 MiB（例如 access 约 21 → 38 MiB、account 约 19 → 45 MiB）。
16. **镜像大小**：4.4 节是压缩大小，解压后 Grafana 2.02 GB、Prometheus 389 MB、Jaeger 180 MB、Alertmanager 133 MB、k6 111 MB。

**压测（3.8 节）**

17. **网关的按 IP 限流**：设计没有考虑到网关每个 IP 最多 50 条 WebSocket 连接、每秒 50 次 HTTP 请求，k6 在单个容器里只有一个来源地址，200 个连接以上会被拒绝。k6 使用固定 IP `172.30.0.20`，网关新增全局规则 `real-ip`（`config.yaml` 启用插件、`apisix.yaml` 加 `global_rules`），只对这个地址信任 `X-Forwarded-For`；k6 为每个虚拟客户端分配 `198.18.0.0/15`（RFC 2544 基准测试网段）中的不同地址。流量仍然经过网关，限流按虚拟客户端生效；account 经网关转发的 `X-Forwarded-For` 看到的也是这些地址。其他来源带的 `X-Forwarded-For` 不改变 `remote_addr`，集群测试中的限流与客户端 IP 用例照常通过。
18. **准备数据改用 k6 脚本**（`test/load/k6/seed.js`，设计写的是 `test/load/seed_test.go`）：从宿主机经网关注册受 `/v1/auth/*` 每个 IP 每分钟 60 次的限制，这个计数在 APISIX 进程内，不能像 account 的计数那样从 Redis 清除，注册 1000 个用户要半小时以上。改在 k6 中按虚拟客户端注册，不需要清除限流计数，1000 个用户约 31 秒。k6 不能直接写文件，准备工作在 `setup()` 中完成，由 `handleSummary()` 写出 `tmp/load/users.json`。
19. **命令**：`make load-up` / `load-restore` 用重建应用容器的方式切换 account 的发送方和采样比例；`load-seed` 先检查 account 使用 `log` 发送方，否则拒绝运行；访问令牌只有 15 分钟，新增 `make load-token` 用刷新令牌换新令牌；击穿实验 `make load-bust` 删除热点群的缓存键。
20. **子协议**：k6/websockets 的 `protocols` 参数尚未实现，脚本直接设置握手头 `Sec-WebSocket-Protocol: marschat.v1.json`。
21. **击穿实验的范围**：删除的是群资料缓存 `social:group:<id>`。开启跨实例重建锁的群成员缓存只在群消息与投递中使用，热点读的接口不读它；social 只有一个实例，跨实例重建锁没有参与，合并回源的是 singleflight。
22. **采样比例的对比**用 0.05 与 0：采样 1.0 时每秒几百条消息会超出 Jaeger 内存存储的容量。

**压测中发现的问题**

23. **并发注册死锁**（TD-40，已修复）：注册事务中吊销设备旧会话的加锁查询查不到行时加间隙锁，新用户都落在会话唯一索引末尾的同一个间隙。注册改为直接创建会话，见 [lessons/innodb-gap-lock-register-deadlock.md](../lessons/innodb-gap-lock-register-deadlock.md)。登录的残留风险记为 TD-43。
24. **连接池上限之和超过 MySQL 最大连接数**（TD-41，已修复）：MySQL 启动参数加 `--max-connections=300`。缩小连接池的方案试过，会让请求排队超时并触发熔断，数据见[重构计划的压测结果](../plans/completed/backend-refactor.md#压测结果)。
25. **delivery 全部停止后没有告警**（TD-42，未处理）：积压指标随 delivery 一起消失，实例注销后 `InstanceDown` 也会结束。
26. **补上 `make dlq`**（2026-11-21，后端收尾复核时发现）：3.5 节的告警表和 `KafkaDeadLetter` 规则的说明都指向 `make dlq`，实施时漏了这个命令，排查的人会被引到不存在的目标。Makefile 补上该目标，命令与 deployment.md“死信队列”一节原来手写的一致。
27. **压测数据改放 `data/load`**（2026-11-21，合并前端分支后整理目录）：`tmp/` 只放临时文件和只读参考源码，可以随时清空；压测准备的用户与令牌要和数据库中的数据对应，结果摘要也要保留，改放被忽略的 `data/load`，k6 容器的挂载、Makefile 与脚本注释同步修改。
