// Package auth 是访问令牌的公共实现：account 用 Signer 签发，各服务用 Server 中间件校验，
// 用 FromContext 取当前用户。令牌为 Ed25519 签名的 JWT，吊销名单存在 Redis。
package auth

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Role 与 account.v1.Role 的取值一致。
const (
	RoleUser  int32 = 1
	RoleAdmin int32 = 2
)

// Principal 是通过校验的当前用户。
type Principal struct {
	UserID    int64
	SessionID int64
	Role      int32
	// IssuedAt 是令牌签发时间，用于和用户级吊销标记比较
	IssuedAt time.Time
}

// IsAdmin 判断是否为管理员。
func (p Principal) IsAdmin() bool {
	return p.Role == RoleAdmin
}

// Claims 是访问令牌的声明。sub 为用户 ID；sid 用字符串，避免前端解析 JWT 时丢失 int64 精度。
type Claims struct {
	jwt.RegisteredClaims
	SessionID int64 `json:"sid,string"`
	Role      int32 `json:"role"`
}

func (c *Claims) principal() (Principal, error) {
	uid, err := strconv.ParseInt(c.Subject, 10, 64)
	if err != nil || uid <= 0 {
		return Principal{}, fmt.Errorf("invalid subject %q", c.Subject)
	}
	if c.SessionID <= 0 || c.IssuedAt == nil {
		return Principal{}, fmt.Errorf("missing sid or iat")
	}
	return Principal{UserID: uid, SessionID: c.SessionID, Role: c.Role, IssuedAt: c.IssuedAt.Time}, nil
}

type principalKey struct{}

// NewContext 把当前用户放进 context。
func NewContext(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// FromContext 取出当前用户；未经过 Server 中间件时 ok 为 false。
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// Signer 签发访问令牌，只在 account 中使用。
type Signer struct {
	key      ed25519.PrivateKey
	keyID    string
	issuer   string
	audience string
	ttl      time.Duration
}

// NewSigner 用 base64 编码的 Ed25519 私钥种子创建签发器。
func NewSigner(seedB64, keyID, issuer, audience string, ttl time.Duration) (*Signer, error) {
	seed, err := base64.StdEncoding.DecodeString(seedB64)
	if err != nil {
		return nil, fmt.Errorf("decode jwt private key: %w", err)
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("jwt private key must be a %d-byte ed25519 seed, got %d bytes", ed25519.SeedSize, len(seed))
	}
	return &Signer{
		key:      ed25519.NewKeyFromSeed(seed),
		keyID:    keyID,
		issuer:   issuer,
		audience: audience,
		ttl:      ttl,
	}, nil
}

// Sign 签发访问令牌，返回令牌和过期时间。
func (s *Signer) Sign(p Principal, now time.Time) (string, time.Time, error) {
	exp := now.Add(s.ttl)
	claims := &Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    s.issuer,
			Subject:   strconv.FormatInt(p.UserID, 10),
			Audience:  jwt.ClaimStrings{s.audience},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
		},
		SessionID: p.SessionID,
		Role:      p.Role,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	token.Header["kid"] = s.keyID
	signed, err := token.SignedString(s.key)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign access token: %w", err)
	}
	return signed, exp, nil
}

// TTL 返回访问令牌有效期。
func (s *Signer) TTL() time.Duration {
	return s.ttl
}

// PublicKey 返回与私钥对应的公钥。
func (s *Signer) PublicKey() ed25519.PublicKey {
	return s.key.Public().(ed25519.PublicKey)
}

// ParsePublicKey 解析 base64 编码的 Ed25519 公钥。
func ParsePublicKey(b64 string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("decode jwt public key: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("jwt public key must be %d bytes, got %d", ed25519.PublicKeySize, len(raw))
	}
	return ed25519.PublicKey(raw), nil
}
