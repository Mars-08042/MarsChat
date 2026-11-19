package server

import (
	"github.com/go-kratos/kratos/v3/middleware/recovery"
	"github.com/go-kratos/kratos/v3/transport/grpc"

	"github.com/Mars-08042/MarsChat/pkg/conf"
)

// NewGRPCServer 创建 gRPC 服务器。Kratos 默认注册 grpc.health.v1 健康服务，
// 服务启动时为 SERVING，停止时切换为 NOT_SERVING。
func NewGRPCServer(c *conf.Server) *grpc.Server {
	opts := []grpc.ServerOption{
		grpc.Middleware(recovery.Recovery()),
	}
	if addr := c.GetGrpc().GetAddr(); addr != "" {
		opts = append(opts, grpc.Address(addr))
	}
	if t := c.GetGrpc().GetTimeout(); t != nil {
		opts = append(opts, grpc.Timeout(t.AsDuration()))
	}
	return grpc.NewServer(opts...)
}
