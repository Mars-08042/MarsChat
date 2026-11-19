package server

import (
	"context"
	"log/slog"
	"strings"

	"github.com/go-kratos/kratos/contrib/middleware/validate/v3"
	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/middleware/selector"
	"github.com/google/wire"
	"github.com/redis/go-redis/v9"

	v1 "github.com/Mars-08042/MarsChat/api/account/v1"
	"github.com/Mars-08042/MarsChat/pkg/auth"
	"github.com/Mars-08042/MarsChat/pkg/conf"
	"github.com/Mars-08042/MarsChat/pkg/middleware/accesslog"
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

// NewMiddlewares 组装中间件，顺序为：访问日志与 panic 恢复 → 令牌校验 → 参数校验。
// 先鉴权再校验参数，未登录的请求拿不到参数校验的错误信息。
func NewMiddlewares(c *conf.Auth, rdb *redis.Client, logger *slog.Logger) (Middlewares, error) {
	authMW, err := auth.Server(c, rdb, logger)
	if err != nil {
		return nil, err
	}
	return Middlewares{
		accesslog.Server(logger),
		selector.Server(authMW).Match(requiresAuth).Build(),
		validate.ProtoValidate(),
	}, nil
}
