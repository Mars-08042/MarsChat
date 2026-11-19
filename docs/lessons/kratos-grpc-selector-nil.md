# Kratos v3 通过服务发现调用 gRPC 时空指针 panic

- 涉及：[pkg/balancer/balancer.go](../../pkg/balancer/balancer.go)、[pkg/bootstrap/registry.go](../../pkg/bootstrap/registry.go)、`github.com/go-kratos/kratos/v3@v3.0.0/transport/grpc/balancer.go`
- 发现时间：2026-11-18，重构步骤 1 运行 `make e2e` 时

## 现象

用 `kgrpc.NewClient(ctx, kgrpc.WithEndpoint("discovery:///marschat-account"), kgrpc.WithDiscovery(r))` 建立连接后，第一次调用就 panic：

```
panic: runtime error: invalid memory address or nil pointer dereference
github.com/go-kratos/kratos/v3/transport/grpc.(*balancerBuilder).Build(...)
	.../kratos/v3@v3.0.0/transport/grpc/balancer.go:52
```

服务端、Consul 注册都正常，直连地址（不经过服务发现）的调用也不受影响。

## 根因

Kratos v3.0.0 的 `transport/grpc` 包里有两个 `init`：

- `balancer.go` 注册名为 `selector` 的 gRPC 负载均衡器，并把 `selector.GlobalSelector()` 的返回值存进 `balancerBuilder.builder`。
- `client.go` 在全局选择器为空时设置默认的 `wrr`。

同一个包内的 `init` 按文件名顺序执行，`balancer.go` 先于 `client.go`，此时全局选择器还没设置，`GlobalSelector()` 返回 nil。之后 `client.go` 虽然设置了全局选择器，但负载均衡器里存的仍是 nil。有可用连接、需要生成 picker 时，`b.builder.Build()` 就对 nil 接口调用了方法。上游 main 分支截至 2026-11-18 仍是这样的写法。

## 解决

新建只依赖 Kratos `selector` 的小包 `pkg/balancer`，在它的 `init` 中调用 `selector.SetGlobalSelector(wrr.NewBuilder())`，并由 `pkg/bootstrap` 匿名导入，所有服务自动带上。

它能先于 `transport/grpc` 执行，依据的是 Go 1.21 起规范定义的包初始化顺序：把所有包按 import 路径排序，每一步初始化排在最前、且依赖都已初始化的包。`github.com/Mars-08042/...` 排在 `github.com/go-kratos/...` 之前，而 `pkg/balancer` 的依赖是 `transport/grpc` 依赖的子集，所以只要 `transport/grpc` 能初始化，`pkg/balancer` 一定已经先初始化了。

在 `main` 或业务包的 `init` 里设置全局选择器没有用：它们依赖 `transport/grpc`，总是在它之后初始化。

## 验证

- 在一个不导入 `pkg/balancer` 的临时包里，用进程内 gRPC 服务和固定实例的服务发现调用健康检查，复现了同样的 panic；加上匿名导入后通过。
- `pkg/balancer` 的单元测试走通“服务发现 → 选节点 → 调用”的路径；`make e2e` 通过 Consul 调用两个服务通过。

## 预防

- 使用 `discovery:///` 地址的程序必须导入 `pkg/bootstrap` 或 `pkg/balancer`，不使用 bootstrap 的测试和工具要显式匿名导入 `pkg/balancer`。
- `pkg/balancer` 不能引入 Kratos `selector` 以外的依赖，否则初始化顺序不再有保证。
- 升级 Kratos 时检查 `transport/grpc/balancer.go` 是否改为在 `Build` 时读取全局选择器，修复后可以删除 `pkg/balancer`。
