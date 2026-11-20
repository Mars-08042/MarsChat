# Redis 容器停止后，go-redis 的拨号重试把每条命令拖到几秒，本该放行的调用变成请求超时

- 涉及：[pkg/bootstrap/redis.go](../../pkg/bootstrap/redis.go)（`NewRedis`、`dialGuard`）、[pkg/bootstrap/redis_test.go](../../pkg/bootstrap/redis_test.go)、[test/cluster/fault_test.go](../../test/cluster/fault_test.go)（`TestFaultStopRedis`、`TestFaultStopRedisLong`）；go-redis `v9.23.0` 的 `options.go`（`DialerRetries`、`DialerRetryTimeout`、`MaxRetries`）、`internal/pool/pool.go`（`dialConn`）
- 发现时间：2026-11-20，重构步骤 5 第 5 段的 Redis 故障实验

## 现象

容器模式下 `docker stop` Redis，设计预期 20 秒内发消息照常成功：限流和令牌吊销检查都是“Redis 故障时放行”。实际停机约 3 秒后，发送开始失败，客户端收到 `503 UPSTREAM_UNAVAILABLE`。message 的日志里同时出现三类记录：

```
"msg":"rate limiter unavailable", ... "error":"ratelimit message:rl:send:...: context canceled"
redis: ... pool.go:770: redis: connection pool: failed to dial after 5 attempts: dial tcp: lookup redis on 127.0.0.11:53: no such host
"msg":"server request", ... "operation":"/message.v1.MessageInternalService/SendMessage","code":500,"reason":"INTERNAL"
```

限流确实放行了，但放行发生在调用方的截止时间（access 调 message 为 3 秒）用完之后。后面的 MySQL 查询拿到的是已经取消的 ctx，于是返回 500；access 那边早已超时，返回 `UPSTREAM_UNAVAILABLE`。

## 根因

三个因素叠加：

1. **服务名从 Docker DNS 中消失，解析变慢**。容器停止后，Compose 网络的内置 DNS（`127.0.0.11`）里不再有 `redis` 这个名字，查询被转发到上游 DNS，约 0.85～1.25 秒后才返回 `no such host`（在同一网络的 MySQL 容器里用 `time getent hosts redis` 实测）。容器运行时解析只要十几毫秒。
2. **go-redis v9.23 的拨号重试**。`DialerRetries` 默认 5，两次尝试之间等 `DialerRetryTimeout`（默认 100 毫秒）。`DialTimeout` 按单次尝试计算，一次拨号最多要 5 次解析加 400 毫秒等待，约 4～6 秒。
3. **命令级重试**。`MaxRetries` 默认 3，连接错误会换一个新连接重试，每次重试都要重新拨号。

结果是 Redis 不可达时，每条命令都要真的拨号才知道失败，而一次拨号就超过了调用方的全部截止时间。“失败就放行”的降级策略因此没有机会生效。在线路由注册、吊销检查、social 的缓存读取都受同样影响。

单元测试里用 miniredis 模拟 Redis 故障时看不到这个问题：本机端口没有监听，连接立即被拒绝，拨号几乎不耗时。

## 解决

在 `pkg/bootstrap.NewRedis` 中处理，所有服务的 Redis 客户端都受益：

- 加一个拨号钩子 `dialGuard`。拨号失败后把 Redis 标记为不可达，期间的拨号不再真的拨号，直接返回上次的错误。后台协程立即探测一次，之后每秒探测一次，探测成功就恢复。每次故障只在开始和恢复时各记一条日志：`redis unreachable, failing dials fast until a probe succeeds`（WARN）、`redis reachable again`（INFO）。
- `DialerRetries` 设为 1。拨号保护生效后，单次拨号内部的重试只会白白等待；连接失败交给命令级重试，而命令级重试的拨号也会立即失败，总共只多出几十毫秒的退避。
- 调用方主动取消导致的拨号失败不触发标记。调用方截止时间太短导致的拨号超时会触发误判，但后台探测立即开始，Redis 正常时几毫秒内恢复，代价很小。

## 验证

- `pkg/bootstrap/redis_test.go`：
  - 模拟每次拨号要等 100 毫秒才失败的 Redis：第一次拨号真的拨号，之后的拨号在 50 毫秒内失败，Redis 恢复后拨号恢复。
  - 调用方取消不触发标记。
  - miniredis 关闭再重启后，客户端在探测间隔内恢复。
  - 去掉快速失败分支后，第一个用例失败。
- 修复后重建镜像，`TestFaultStopRedis` 和 `TestFaultStopRedisLong` 通过：
  - 停机 20 秒期间发送全部成功；
  - 停机 60 秒期间，已有会话的发送全部成功，各进程在每次故障中各记一条“不可达”和一条“已恢复”。

## 预防

- 依赖“失败就放行”的调用，要确认失败能在调用方截止时间内发生。用 miniredis 关掉端口得到的是“立即被拒绝”，不能代表真实环境中主机消失、DNS 变慢或网络丢包时的表现。这类降级要在容器环境里做故障实验。
- 升级 go-redis 时留意默认值的变化。`DialerRetries` 是 v9 后期加入的选项，默认 5 次，旧版本没有单次拨号内的重试。
