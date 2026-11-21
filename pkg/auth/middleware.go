package auth

import (
	"context"
	"log/slog"

	jwtmw "github.com/go-kratos/kratos/contrib/middleware/jwt/v3"
	kerrors "github.com/go-kratos/kratos/v3/errors"
	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"

	"github.com/Mars-08042/MarsChat/pkg/conf"
)

// ErrUnauthorized 与 Kratos JWT contrib 的错误保持一致（401，reason 为 UNAUTHORIZED），
// 客户端对所有 401 统一处理：先刷新一次令牌，失败再回到登录页。
func ErrUnauthorized(message string) *kerrors.Error {
	return kerrors.Unauthorized("UNAUTHORIZED", message)
}

// Server 返回校验访问令牌的服务端中间件：验签与声明校验 → 吊销检查 → 把 Principal 放进 context。
// 不需要鉴权的接口由调用方用 Kratos selector 跳过。
func Server(c *conf.Auth, rdb redis.UniversalClient, logger *slog.Logger) (middleware.Middleware, error) {
	pub, err := ParsePublicKey(c.GetPublicKey())
	if err != nil {
		return nil, err
	}
	verify := jwtmw.Server(
		func(*jwt.Token) (any, error) { return pub, nil },
		jwtmw.WithSigningMethod(jwt.SigningMethodEdDSA),
		jwtmw.WithClaims(func() jwt.Claims { return &Claims{} }),
		jwtmw.WithParserOptions(parserOptions(c)...),
	)
	revocations := NewRevocations(rdb)
	check := func(handler middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (any, error) {
			claims, ok := jwtmw.FromContext(ctx)
			if !ok {
				return nil, ErrUnauthorized("missing token claims")
			}
			cl, ok := claims.(*Claims)
			if !ok {
				return nil, ErrUnauthorized("unexpected token claims")
			}
			p, err := cl.principal()
			if err != nil {
				return nil, ErrUnauthorized("invalid token claims")
			}
			if err := checkRevoked(ctx, revocations, p, logger); err != nil {
				return nil, err
			}
			return handler(NewContext(ctx, p), req)
		}
	}
	return middleware.Chain(verify, check), nil
}
