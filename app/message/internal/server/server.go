package server

import (
	"context"
	"log/slog"
	"strings"

	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/transport/grpc"
	"github.com/go-kratos/kratos/v3/transport/http"
	"github.com/google/wire"
	"github.com/redis/go-redis/v9"

	v1 "github.com/Mars-08042/MarsChat/api/message/v1"
	"github.com/Mars-08042/MarsChat/app/message/internal/data"
	"github.com/Mars-08042/MarsChat/app/message/internal/service"
	"github.com/Mars-08042/MarsChat/pkg/bootstrap"
	"github.com/Mars-08042/MarsChat/pkg/conf"
	"github.com/Mars-08042/MarsChat/pkg/telemetry"
)

// ProviderSet 是 server 层的依赖提供者。
var ProviderSet = wire.NewSet(NewMiddlewares, NewGRPCServer, NewHTTPServer, NewSocialEventConsumer)

// Middlewares 是 HTTP 与 gRPC 共用的中间件链。
type Middlewares []middleware.Middleware

// requiresAuth：MessageService 的接口全部需要登录。MessageInternalService 只在内网 gRPC 上提供，
// 调用方是 access，发送者取自 access 上已鉴权的连接（服务间认证见 TD-26）。
func requiresAuth(_ context.Context, operation string) bool {
	return strings.HasPrefix(operation, "/message.v1.MessageService/")
}

// NewMiddlewares 组装公共中间件链，见 bootstrap.ServerMiddlewares。
func NewMiddlewares(c *conf.Auth, rdb *redis.Client, logger *slog.Logger) (Middlewares, error) {
	return bootstrap.ServerMiddlewares(c, rdb, logger, requiresAuth)
}

// NewHTTPServer 创建 HTTP 服务器，并挂载存活探针 /healthz、就绪探针 /readyz 和指标端点 /metrics。
func NewHTTPServer(c *conf.Server, mws Middlewares, d *data.Data, msg *service.MessageService, logger *slog.Logger) *http.Server {
	opts := []http.ServerOption{
		http.Middleware(mws...),
	}
	if addr := c.GetHttp().GetAddr(); addr != "" {
		opts = append(opts, http.Address(addr))
	}
	if t := c.GetHttp().GetTimeout(); t != nil {
		opts = append(opts, http.Timeout(t.AsDuration()))
	}
	srv := http.NewServer(opts...)
	srv.HandleFunc("/healthz", bootstrap.LivenessHandler)
	srv.Handle("/readyz", bootstrap.ReadinessHandler(logger, d.ReadinessChecks()...))
	// 指标端点同探针一样不经过中间件；网关只转发 /v1/... 与 /ws，外部访问不到
	srv.Handle("/metrics", telemetry.MetricsHandler())
	v1.RegisterMessageServiceHTTPServer(srv, msg)
	return srv
}

// NewGRPCServer 创建 gRPC 服务器，同时提供对外接口和内部接口。
func NewGRPCServer(c *conf.Server, mws Middlewares, msg *service.MessageService, internal *service.InternalService) *grpc.Server {
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
	v1.RegisterMessageServiceServer(srv, msg)
	v1.RegisterMessageInternalServiceServer(srv, internal)
	return srv
}
