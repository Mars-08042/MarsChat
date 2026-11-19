package server

import (
	"context"
	"log/slog"
	"strings"

	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/google/wire"
	"github.com/redis/go-redis/v9"

	v1 "github.com/Mars-08042/MarsChat/api/account/v1"
	"github.com/Mars-08042/MarsChat/pkg/bootstrap"
	"github.com/Mars-08042/MarsChat/pkg/conf"
)

// ProviderSet 是 server 层的依赖提供者。
var ProviderSet = wire.NewSet(NewMiddlewares, NewGRPCServer, NewHTTPServer)

// Middlewares 是 HTTP 与 gRPC 共用的中间件链。
type Middlewares []middleware.Middleware

// 不需要登录的接口。AccountInternalService 只在内网 gRPC 上提供，也不经过令牌校验
var publicOperations = map[string]bool{
	v1.OperationAccountServiceSendSmsCode:     true,
	v1.OperationAccountServiceRegister:        true,
	v1.OperationAccountServiceLoginByPassword: true,
	v1.OperationAccountServiceLoginBySms:      true,
	v1.OperationAccountServiceRefreshToken:    true,
}

// requiresAuth 用白名单表示公开接口：AccountService 新增的接口默认需要登录，忘记配置时是安全的一侧。
func requiresAuth(_ context.Context, operation string) bool {
	return strings.HasPrefix(operation, "/account.v1.AccountService/") && !publicOperations[operation]
}

// NewMiddlewares 组装公共中间件链，见 bootstrap.ServerMiddlewares。
func NewMiddlewares(c *conf.Auth, rdb *redis.Client, logger *slog.Logger) (Middlewares, error) {
	return bootstrap.ServerMiddlewares(c, rdb, logger, requiresAuth)
}
