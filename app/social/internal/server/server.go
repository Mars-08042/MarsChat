package server

import (
	"context"
	"log/slog"
	"strings"

	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/google/wire"
	"github.com/redis/go-redis/v9"

	"github.com/Mars-08042/MarsChat/pkg/bootstrap"
	"github.com/Mars-08042/MarsChat/pkg/conf"
)

// ProviderSet 是 server 层的依赖提供者。
var ProviderSet = wire.NewSet(NewMiddlewares, NewGRPCServer, NewHTTPServer, NewCacheEventConsumer)

// Middlewares 是 HTTP 与 gRPC 共用的中间件链。
type Middlewares []middleware.Middleware

// requiresAuth：SocialService 的接口全部需要登录。SocialInternalService 只在内网 gRPC 上提供，
// 调用方是其他服务而不是用户，不经过令牌校验（服务间认证见 TD-26）。
func requiresAuth(_ context.Context, operation string) bool {
	return strings.HasPrefix(operation, "/social.v1.SocialService/")
}

// NewMiddlewares 组装公共中间件链，见 bootstrap.ServerMiddlewares。
func NewMiddlewares(c *conf.Auth, rdb *redis.Client, logger *slog.Logger) (Middlewares, error) {
	return bootstrap.ServerMiddlewares(c, rdb, logger, requiresAuth)
}
