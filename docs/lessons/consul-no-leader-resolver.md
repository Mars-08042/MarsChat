# Consul 没有 leader 时启动的服务，gRPC 服务发现永久失效

- 涉及：[pkg/bootstrap/registry.go](../../pkg/bootstrap/registry.go)、[app/access/internal/data/data.go](../../app/access/internal/data/data.go)、`github.com/go-kratos/kratos/contrib/registry/consul/v3`（`registry.go` 的 `Watch` 与 `resolve`）、`github.com/go-kratos/kratos/v3@v3.0.0/transport/grpc/client.go`
- 发现时间：2026-11-21，前端第 4 阶段联调
- 技术债：TD-44

## 现象

前端发出的消息全部失败，提示「服务暂时不可用」。access 日志：

```
send message failed ... reason = UPSTREAM_UNAVAILABLE ... cause = rpc error: code = DeadlineExceeded desc = context deadline exceeded
```

- 两个 access 实例都一样；message 自身没有任何错误日志，HTTP 接口（会话列表、历史消息）正常。
- Jaeger 中这次调用只有 access 一侧的两个 span，请求没有到达 message。
- access 这次启动后的日志里没有任何 `[resolver] update instances`，Consul 的访问日志里也没有 access 查询 `marschat-message` 的记录。
- Consul 中 message 的两个实例都是 passing。

## 根因

1. 本机 Docker 重启，所有容器同时启动。Consul 在 04:19:55 启动，04:20:01 才选出 leader；access 和 delivery 在 04:19:56 启动，message、social 在 04:20:02 之后。
2. Kratos v3 的 `transport/grpc.NewClient` 创建连接后立即调用 `conn.Connect()`，gRPC 马上构建解析器，解析器调用 `Registry.Watch`。
3. contrib 的 `Watch`：先在 `r.registry[name]` 登记一个 `serviceSet`，再调用 `resolve` 做第一次查询并启动后台轮询。没有 leader 时健康查询返回错误，`Watch` 把错误返回给解析器，但登记没有撤销，后台轮询也没启动。
4. gRPC 之后重建解析器时再次 `Watch`：发现登记已经存在，就不再查询，直接返回一个 watcher。这个 `serviceSet` 里没有实例、也没有轮询，`Next()` 永远阻塞。调用一直等到超时，表现为 `DeadlineExceeded`。
5. 只有进程重启才会清掉这个登记。message 到 social 的调用正常，因为 message 是在 leader 选出之后才启动的。

`compose.yaml` 里 consul 的健康检查是 `consul members`，agent 启动就通过，不等 leader，所以 `depends_on: service_healthy` 也挡不住这个窗口；Docker 重启时更是不管 `depends_on`。

## 排查要点

- 先看 Jaeger：只有调用方的 span，说明请求没有发出去，问题在调用方的连接或服务发现，而不是被调方。
- 对比各容器的启动时间和 Consul 的选主时间（`docker logs marschat-consul-1` 里的 `cluster leadership acquired`）。
- 解析器拿到实例时会打印 `[resolver] update instances`；某个下游从来没有这条日志，就是解析器没拿到过实例。

## 解决

临时：重启受影响的服务，`docker compose --profile apps restart access delivery`。

根治（待做，见 TD-44）：`pkg/bootstrap.NewConsulRegistry` 返回前等 Consul 有 leader；consul 的健康检查改为确认已有 leader。
