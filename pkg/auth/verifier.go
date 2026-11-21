package auth

import (
	"context"
	"crypto/ed25519"
	"log/slog"

	"github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"

	"github.com/Mars-08042/MarsChat/pkg/conf"
)

// parserOptions 是解析访问令牌的公共规则，Server 中间件与 Verifier 共用，
// 保证 HTTP、gRPC 和 WebSocket 的令牌校验完全一致。
//
// 在解析阶段就只接受 EdDSA，防止算法混淆攻击（contrib 中间件先按令牌头部声明的算法解析，之后才比对算法）。
func parserOptions(c *conf.Auth) []jwt.ParserOption {
	return []jwt.ParserOption{
		jwt.WithValidMethods([]string{jwt.SigningMethodEdDSA.Alg()}),
		jwt.WithIssuer(c.GetIssuer()),
		jwt.WithAudience(c.GetAudience()),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
	}
}

// checkRevoked 查吊销名单。Redis 故障时放行并记 WARN（开发者确认的取舍）：
// 访问令牌最长有效 15 分钟，刷新时仍会查数据库。
func checkRevoked(ctx context.Context, r *Revocations, p Principal, logger *slog.Logger) error {
	revoked, err := r.IsRevoked(ctx, p)
	if err != nil {
		logger.Warn("revocation check failed, allowing request", slog.Int64("user_id", p.UserID), slog.Any("error", err))
		return nil
	}
	if revoked {
		return ErrUnauthorized("token revoked")
	}
	return nil
}

// Verifier 校验原始的令牌字符串，用于拿不到 Authorization 头的场景：浏览器建立 WebSocket 时不能设置请求头，
// 令牌放在连接的第一帧里（设计 message-pipeline 3.3）。规则与 Server 中间件相同。
type Verifier struct {
	pub         ed25519.PublicKey
	parser      *jwt.Parser
	revocations *Revocations
	logger      *slog.Logger
}

// NewVerifier 创建校验器。
func NewVerifier(c *conf.Auth, rdb redis.UniversalClient, logger *slog.Logger) (*Verifier, error) {
	pub, err := ParsePublicKey(c.GetPublicKey())
	if err != nil {
		return nil, err
	}
	return &Verifier{
		pub:         pub,
		parser:      jwt.NewParser(parserOptions(c)...),
		revocations: NewRevocations(rdb),
		logger:      logger,
	}, nil
}

// Authenticate 校验签名、声明和吊销名单，返回当前用户。失败时返回 401 UNAUTHORIZED。
func (v *Verifier) Authenticate(ctx context.Context, token string) (Principal, error) {
	cl := &Claims{}
	if _, err := v.parser.ParseWithClaims(token, cl, func(*jwt.Token) (any, error) { return v.pub, nil }); err != nil {
		return Principal{}, ErrUnauthorized("invalid token")
	}
	p, err := cl.principal()
	if err != nil {
		return Principal{}, ErrUnauthorized("invalid token claims")
	}
	if err := checkRevoked(ctx, v.revocations, p, v.logger); err != nil {
		return Principal{}, err
	}
	return p, nil
}

// IsRevoked 复查已建立连接的令牌是否被吊销，长连接每次心跳调用一次。
// 返回的错误表示 Redis 不可用，调用方按放行处理。
func (v *Verifier) IsRevoked(ctx context.Context, p Principal) (bool, error) {
	return v.revocations.IsRevoked(ctx, p)
}
