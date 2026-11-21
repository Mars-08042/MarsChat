package bootstrap

import (
	"log/slog"

	"github.com/go-kratos/kratos/contrib/middleware/validate/v3"
	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/middleware/selector"
	"github.com/redis/go-redis/v9"

	"github.com/Mars-08042/MarsChat/pkg/auth"
	"github.com/Mars-08042/MarsChat/pkg/conf"
	"github.com/Mars-08042/MarsChat/pkg/middleware/accesslog"
	"github.com/Mars-08042/MarsChat/pkg/telemetry"
)

// ServerMiddlewares 组装 HTTP 与 gRPC 共用的服务端中间件，顺序为：
// 链路追踪 → 请求指标 → 访问日志与 panic 恢复 → 令牌校验（requiresAuth 返回 true 的操作）→ 参数校验。
//
// 不使用 Kratos 自带的 logging、recovery：它们会把请求体写进日志，
// 见 docs/lessons/kratos-v3-server-defaults.md。先鉴权再校验参数，未登录的请求拿不到参数校验的错误信息。
func ServerMiddlewares(c *conf.Auth, rdb redis.UniversalClient, logger *slog.Logger, requiresAuth selector.MatchFunc) ([]middleware.Middleware, error) {
	authMW, err := auth.Server(c, rdb, logger)
	if err != nil {
		return nil, err
	}
	mws, err := telemetry.ServerMiddlewares()
	if err != nil {
		return nil, err
	}
	return append(mws,
		accesslog.Server(logger),
		selector.Server(authMW).Match(requiresAuth).Build(),
		validate.ProtoValidate(),
	), nil
}
