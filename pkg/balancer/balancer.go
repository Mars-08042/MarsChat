// Package balancer 为 Kratos gRPC 客户端设置全局负载均衡选择器，使用方只需匿名导入：
//
//	import _ "github.com/Mars-08042/MarsChat/pkg/balancer"
//
// 为什么需要它：Kratos v3.0.0 的 transport/grpc 在 balancer.go 的 init 中注册负载均衡器时，
// 就把 selector.GlobalSelector() 的结果存进了结构体；默认选择器却在同一个包的 client.go 的
// init 中才设置。同一个包内的 init 按文件名顺序执行，balancer.go 在前，存下的是 nil，
// 通过 discovery:/// 地址发起调用、开始选节点时就会空指针 panic（上游 main 分支截至 2026-11-18 未修复）。
//
// 为什么这样能修复：Go 1.21 起，包初始化顺序是“按 import 路径排序，每一步初始化排在最前且依赖都已
// 初始化的包”。本包的 import 路径排在 github.com/go-kratos 之前，依赖又只有 Kratos 的 selector，
// 是 transport/grpc 依赖的子集，所以一定先于 transport/grpc 初始化。因此本包不能再引入其他依赖。
package balancer

import (
	"github.com/go-kratos/kratos/v3/selector"
	"github.com/go-kratos/kratos/v3/selector/wrr"
)

func init() {
	// 与 Kratos 原本的默认值一致：加权轮询
	if selector.GlobalSelector() == nil {
		selector.SetGlobalSelector(wrr.NewBuilder())
	}
}
