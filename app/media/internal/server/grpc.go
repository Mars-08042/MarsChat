package server

import (
	"github.com/go-kratos/kratos/v3/transport/grpc"

	v1 "github.com/Mars-08042/MarsChat/api/media/v1"
	"github.com/Mars-08042/MarsChat/app/media/internal/service"
	"github.com/Mars-08042/MarsChat/pkg/conf"
)

// NewGRPCServer 创建 gRPC 服务器，同时提供对外接口和内部接口（上传只有 HTTP）。Kratos 默认注册 grpc.health.v1
// 健康服务，服务启动时为 SERVING，停止时切换为 NOT_SERVING。
func NewGRPCServer(c *conf.Server, mws Middlewares, media *service.MediaService, internal *service.InternalService) *grpc.Server {
	opts := []grpc.ServerOption{
		grpc.Middleware(mws...),
	}
	if addr := c.GetGrpc().GetAddr(); addr != "" {
		opts = append(opts, grpc.Address(addr))
	}
	if t := c.GetGrpc().GetTimeout(); t != nil {
		opts = append(opts, grpc.Timeout(t.AsDuration()))
	}
	srv := grpc.NewServer(opts...)
	v1.RegisterMediaServiceServer(srv, media)
	v1.RegisterMediaInternalServiceServer(srv, internal)
	return srv
}
