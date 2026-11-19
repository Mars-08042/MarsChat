package biz

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Mars-08042/MarsChat/app/account/internal/conf"
	"github.com/Mars-08042/MarsChat/pkg/auth"
	pkgconf "github.com/Mars-08042/MarsChat/pkg/conf"
)

// TokenPair 是返回给客户端的令牌对。
type TokenPair struct {
	AccessToken      string
	AccessExpiresAt  time.Time
	RefreshToken     string
	RefreshExpiresAt time.Time
}

// TokenService 签发访问令牌、生成和解析刷新令牌。
type TokenService struct {
	signer        *auth.Signer
	refreshTTL    time.Duration
	rotationGrace time.Duration
	revocationTTL time.Duration
}

// NewTokenService 校验令牌配置：吊销记录的 TTL 必须覆盖访问令牌有效期；配置的公钥必须与私钥匹配，
// 否则其他服务会拒绝 account 签发的全部令牌，这类错误在启动时暴露比上线后才发现好。
func NewTokenService(c *conf.Token, a *pkgconf.Auth) (*TokenService, error) {
	accessTTL := c.GetAccessTtl().AsDuration()
	revocationTTL := c.GetRevocationTtl().AsDuration()
	if revocationTTL < accessTTL {
		return nil, fmt.Errorf("token.revocation_ttl (%s) must not be shorter than token.access_ttl (%s)", revocationTTL, accessTTL)
	}
	signer, err := auth.NewSigner(c.GetPrivateKey(), c.GetKeyId(), a.GetIssuer(), a.GetAudience(), accessTTL)
	if err != nil {
		return nil, err
	}
	pub, err := auth.ParsePublicKey(a.GetPublicKey())
	if err != nil {
		return nil, err
	}
	if !pub.Equal(signer.PublicKey()) {
		return nil, errors.New("auth.public_key does not match token.private_key")
	}
	return &TokenService{
		signer:        signer,
		refreshTTL:    c.GetRefreshTtl().AsDuration(),
		rotationGrace: c.GetRotationGrace().AsDuration(),
		revocationTTL: revocationTTL,
	}, nil
}

// RevocationTTL 是吊销记录的保留时间。
func (t *TokenService) RevocationTTL() time.Duration {
	return t.revocationTTL
}

// newRefreshSecret 生成刷新令牌的随机部分及其哈希。
func newRefreshSecret() (secret string, hash []byte, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	secret = base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(secret))
	return secret, sum[:], nil
}

// issue 签发访问令牌，并把会话 ID 和随机部分拼成刷新令牌。
func (t *TokenService) issue(p auth.Principal, refreshSecret string, refreshExpiresAt, now time.Time) (*TokenPair, error) {
	access, exp, err := t.signer.Sign(p, now)
	if err != nil {
		return nil, err
	}
	return &TokenPair{
		AccessToken:      access,
		AccessExpiresAt:  exp,
		RefreshToken:     strconv.FormatInt(p.SessionID, 10) + "." + refreshSecret,
		RefreshExpiresAt: refreshExpiresAt,
	}, nil
}

// parseRefreshToken 拆出会话 ID 和随机部分的哈希。
func parseRefreshToken(token string) (sessionID int64, hash []byte, ok bool) {
	sid, secret, found := strings.Cut(token, ".")
	if !found || secret == "" {
		return 0, nil, false
	}
	id, err := strconv.ParseInt(sid, 10, 64)
	if err != nil || id <= 0 {
		return 0, nil, false
	}
	sum := sha256.Sum256([]byte(secret))
	return id, sum[:], true
}

// hashEqual 用常数时间比较，避免通过响应时间逐字节猜出哈希。
func hashEqual(a, b []byte) bool {
	return len(a) > 0 && subtle.ConstantTimeCompare(a, b) == 1
}
