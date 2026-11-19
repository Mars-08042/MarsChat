package auth

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	kerrors "github.com/go-kratos/kratos/v3/errors"
	"github.com/go-kratos/kratos/v3/transport"
	"github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"

	"github.com/Mars-08042/MarsChat/pkg/conf"
)

// fakeTransport 模拟服务端请求上下文，只需要请求头。
type fakeTransport struct{ header http.Header }

type headerCarrier http.Header

func (h headerCarrier) Get(k string) string      { return http.Header(h).Get(k) }
func (h headerCarrier) Set(k, v string)          { http.Header(h).Set(k, v) }
func (h headerCarrier) Add(k, v string)          { http.Header(h).Add(k, v) }
func (h headerCarrier) Keys() []string           { return nil }
func (h headerCarrier) Values(k string) []string { return http.Header(h).Values(k) }

func (t fakeTransport) Kind() transport.Kind            { return transport.KindHTTP }
func (t fakeTransport) Endpoint() string                { return "" }
func (t fakeTransport) Operation() string               { return "/test" }
func (t fakeTransport) RequestHeader() transport.Header { return headerCarrier(t.header) }
func (t fakeTransport) ReplyHeader() transport.Header   { return headerCarrier(http.Header{}) }

const (
	testIssuer   = "marschat-account"
	testAudience = "marschat"
)

type fixture struct {
	signer *Signer
	mr     *miniredis.Miniredis
	rdb    *redis.Client
	call   func(token string) (Principal, error)
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i)
	}
	signer, err := NewSigner(base64.StdEncoding.EncodeToString(seed), "k1", testIssuer, testAudience, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	mw, err := Server(&conf.Auth{
		PublicKey: base64.StdEncoding.EncodeToString(signer.PublicKey()),
		Issuer:    testIssuer,
		Audience:  testAudience,
	}, rdb, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	h := mw(func(ctx context.Context, _ any) (any, error) {
		p, _ := FromContext(ctx)
		return p, nil
	})
	call := func(token string) (Principal, error) {
		ctx := transport.NewServerContext(context.Background(), fakeTransport{header: http.Header{"Authorization": {"Bearer " + token}}})
		out, err := h(ctx, nil)
		if err != nil {
			return Principal{}, err
		}
		return out.(Principal), nil
	}
	return &fixture{signer: signer, mr: mr, rdb: rdb, call: call}
}

func assertUnauthorized(t *testing.T, err error) {
	t.Helper()
	if e := kerrors.FromError(err); e == nil || e.Code != 401 {
		t.Fatalf("err = %v, want 401", err)
	}
}

func TestValidToken(t *testing.T) {
	f := newFixture(t)
	token, _, err := f.signer.Sign(Principal{UserID: 42, SessionID: 7, Role: RoleAdmin}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.call(token)
	if err != nil {
		t.Fatal(err)
	}
	if p.UserID != 42 || p.SessionID != 7 || !p.IsAdmin() {
		t.Fatalf("principal = %+v", p)
	}
}

func TestExpiredToken(t *testing.T) {
	f := newFixture(t)
	token, _, _ := f.signer.Sign(Principal{UserID: 42, SessionID: 7}, time.Now().Add(-time.Hour))
	_, err := f.call(token)
	assertUnauthorized(t, err)
}

func TestTamperedToken(t *testing.T) {
	f := newFixture(t)
	token, _, _ := f.signer.Sign(Principal{UserID: 42, SessionID: 7}, time.Now())
	parts := strings.Split(token, ".")
	// 换成另一个用户的声明，签名不再匹配
	other, _, _ := f.signer.Sign(Principal{UserID: 43, SessionID: 7}, time.Now())
	parts[1] = strings.Split(other, ".")[1]
	parts[2] = strings.Split(token, ".")[2]
	_, err := f.call(strings.Join(parts, "."))
	assertUnauthorized(t, err)
}

// 算法混淆：用公钥字节作为 HMAC 密钥签名的 HS256 令牌必须被拒绝。
func TestRejectsOtherAlgorithms(t *testing.T) {
	f := newFixture(t)
	claims := &Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: testIssuer, Subject: "42", Audience: jwt.ClaimStrings{testAudience},
			IssuedAt: jwt.NewNumericDate(time.Now()), ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
		},
		SessionID: 7,
	}
	forged, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(f.signer.PublicKey()))
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.call(forged)
	assertUnauthorized(t, err)
}

func TestWrongAudience(t *testing.T) {
	f := newFixture(t)
	other, _ := NewSigner(base64.StdEncoding.EncodeToString(make([]byte, 32)), "k1", testIssuer, "someone-else", time.Minute)
	token, _, _ := other.Sign(Principal{UserID: 42, SessionID: 7}, time.Now())
	_, err := f.call(token)
	assertUnauthorized(t, err)
}

func TestRevokedSession(t *testing.T) {
	f := newFixture(t)
	token, _, _ := f.signer.Sign(Principal{UserID: 42, SessionID: 7}, time.Now())
	if err := NewRevocations(f.rdb).RevokeSessions(context.Background(), time.Hour, 7); err != nil {
		t.Fatal(err)
	}
	_, err := f.call(token)
	assertUnauthorized(t, err)
}

// 用户级吊销只影响吊销时间及之前签发的令牌。
func TestRevokedUser(t *testing.T) {
	f := newFixture(t)
	now := time.Now()
	before, _, _ := f.signer.Sign(Principal{UserID: 42, SessionID: 7}, now.Add(-time.Minute))
	// 吊销发生在 10 秒前；签发时间不能在未来（jwt.WithIssuedAt 会拒绝）
	if err := NewRevocations(f.rdb).RevokeUser(context.Background(), 42, now.Add(-10*time.Second), time.Hour); err != nil {
		t.Fatal(err)
	}
	_, err := f.call(before)
	assertUnauthorized(t, err)

	after, _, _ := f.signer.Sign(Principal{UserID: 42, SessionID: 8}, now)
	if _, err := f.call(after); err != nil {
		t.Fatalf("token issued after revocation rejected: %v", err)
	}
}

// Redis 故障时放行。
func TestRedisDownAllows(t *testing.T) {
	f := newFixture(t)
	token, _, _ := f.signer.Sign(Principal{UserID: 42, SessionID: 7}, time.Now())
	f.mr.Close()
	if _, err := f.call(token); err != nil {
		t.Fatalf("want allowed when redis is down, got %v", err)
	}
}

func TestMissingToken(t *testing.T) {
	f := newFixture(t)
	_, err := f.call("")
	assertUnauthorized(t, err)
}
