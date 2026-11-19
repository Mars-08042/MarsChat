package biz

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	kerrors "github.com/go-kratos/kratos/v3/errors"
	"google.golang.org/protobuf/types/known/durationpb"

	v1 "github.com/Mars-08042/MarsChat/api/account/v1"
	"github.com/Mars-08042/MarsChat/app/account/internal/conf"
	"github.com/Mars-08042/MarsChat/pkg/auth"
	pkgconf "github.com/Mars-08042/MarsChat/pkg/conf"
)

const (
	testCode  = "123456"
	testPhone = "13800000001"
	testPass  = "abc12345"
)

var testDevice = Device{ID: "device-web-1", Platform: 1, Name: "Chrome"}

type env struct {
	users    *fakeUsers
	sessions *fakeSessions
	events   *fakeEvents
	store    *fakeSmsStore
	guard    *fakeGuard
	limiter  *fakeLimiter
	sender   *fakeSender
	revoker  *fakeRevoker
	clock    time.Time

	sms     *SmsUsecase
	auth    *AuthUsecase
	user    *UserUsecase
	session *SessionUsecase
	admin   *AdminUsecase
}

func testPolicy() *conf.Policy {
	window := &pkgconf.RateLimit{Limit: 20, Window: durationpb.New(time.Hour)}
	return &conf.Policy{
		Sms: &conf.Policy_Sms{
			CodeTtl: durationpb.New(5 * time.Minute), MaxVerifyAttempts: 3,
			SendInterval: durationpb.New(time.Minute), DailyLimitPerPhone: 10, IpLimit: window,
		},
		Login: &conf.Policy_Login{
			MaxFailures: 3, FailureWindow: durationpb.New(15 * time.Minute),
			LockDuration: durationpb.New(15 * time.Minute), IpLimit: window,
		},
	}
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := NewTokenService(&conf.Token{
		PrivateKey: base64.StdEncoding.EncodeToString(priv.Seed()), KeyId: "test",
		AccessTtl: durationpb.New(15 * time.Minute), RefreshTtl: durationpb.New(30 * 24 * time.Hour),
		RotationGrace: durationpb.New(10 * time.Second), RevocationTtl: durationpb.New(time.Hour),
	}, &pkgconf.Auth{PublicKey: base64.StdEncoding.EncodeToString(pub), Issuer: "iss", Audience: "aud"})
	if err != nil {
		t.Fatal(err)
	}

	e := &env{
		users: newFakeUsers(), sessions: newFakeSessions(), events: &fakeEvents{}, store: newFakeSmsStore(),
		guard: &fakeGuard{failures: map[string]int{}}, limiter: &fakeLimiter{}, sender: &fakeSender{},
		revoker: &fakeRevoker{users: map[int64]time.Time{}},
		clock:   time.Date(2026, 11, 18, 8, 0, 0, 0, time.UTC),
	}
	now := func() time.Time { return e.clock }
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	policy := NewPolicyHolder(testPolicy())
	ids := &fakeIDs{}

	e.sms, err = NewSmsUsecase(e.store, e.sender, e.limiter, e.users, policy, &conf.Sms{Provider: "log", FixedCode: testCode}, logger)
	if err != nil {
		t.Fatal(err)
	}
	e.sms.now = now
	// bcrypt 用最低 cost，测试只关心逻辑
	e.auth, err = NewAuthUsecase(e.users, e.sessions, e.events, fakeTx{}, tokens, e.sms, e.limiter, e.guard, ids,
		e.revoker, policy, &conf.Security{BcryptCost: 4}, logger)
	if err != nil {
		t.Fatal(err)
	}
	e.auth.now = now
	e.user = NewUserUsecase(e.users, logger)
	e.user.now = now
	e.session = NewSessionUsecase(e.sessions, e.events, fakeTx{}, tokens, e.revoker, logger)
	e.session.now = now
	e.admin = NewAdminUsecase(e.users, e.sessions, e.events, fakeTx{}, tokens, e.revoker, logger)
	e.admin.now = now
	return e
}

// register 发送验证码并注册，返回用户和令牌。
func (e *env) register(t *testing.T, phone string) (*User, *TokenPair) {
	t.Helper()
	ctx := context.Background()
	if _, err := e.sms.Send(ctx, phone, PurposeRegister, "1.2.3.4"); err != nil {
		t.Fatalf("send register code: %v", err)
	}
	u, pair, err := e.auth.Register(ctx, RegisterInput{
		Phone: phone, Code: testCode, Password: testPass, Nickname: "nick", Device: testDevice, IP: "1.2.3.4",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	return u, pair
}

func principalOf(u *User, pair *TokenPair) auth.Principal {
	sid, _, _ := parseRefreshToken(pair.RefreshToken)
	return auth.Principal{UserID: u.ID, SessionID: sid, Role: u.Role}
}

func wantReason(t *testing.T, err error, reason v1.ErrorReason) {
	t.Helper()
	if got := kerrors.Reason(err); got != reason.String() {
		t.Fatalf("error = %v, want reason %s", err, reason)
	}
}

func TestRegisterAndPasswordLogin(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u, pair := e.register(t, testPhone)
	if u.Role != RoleUser || u.Status != StatusNormal || pair.AccessToken == "" {
		t.Fatalf("unexpected user %+v / pair %+v", u, pair)
	}
	if len(e.users.registered) != 1 {
		t.Fatalf("bloom filter not notified: %v", e.users.registered)
	}

	// 注册码只能用一次
	_, _, err := e.auth.Register(ctx, RegisterInput{Phone: testPhone, Code: testCode, Password: testPass, Nickname: "n", Device: testDevice})
	wantReason(t, err, v1.ErrorReason_SMS_CODE_INVALID)

	// 已注册的手机号不能再发注册码
	_, err = e.sms.Send(ctx, testPhone, PurposeRegister, "1.2.3.4")
	wantReason(t, err, v1.ErrorReason_PHONE_ALREADY_REGISTERED)

	if _, _, err := e.auth.LoginByPassword(ctx, testPhone, testPass, Device{ID: "device-ios-1", Platform: 3}, "1.2.3.4"); err != nil {
		t.Fatalf("login: %v", err)
	}
	_, _, err = e.auth.LoginByPassword(ctx, testPhone, "wrong1234", testDevice, "1.2.3.4")
	wantReason(t, err, v1.ErrorReason_INVALID_CREDENTIALS)
	// 不存在的手机号与密码错误返回同样的错误
	_, _, err = e.auth.LoginByPassword(ctx, "13900000000", testPass, testDevice, "1.2.3.4")
	wantReason(t, err, v1.ErrorReason_INVALID_CREDENTIALS)
}

func TestRegisterRejectsWeakPasswordWithoutConsumingCode(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.sms.Send(ctx, testPhone, PurposeRegister, ""); err != nil {
		t.Fatal(err)
	}
	_, _, err := e.auth.Register(ctx, RegisterInput{Phone: testPhone, Code: testCode, Password: "abcdefgh", Nickname: "n", Device: testDevice})
	wantReason(t, err, v1.ErrorReason_WEAK_PASSWORD)
	if _, _, err := e.auth.Register(ctx, RegisterInput{Phone: testPhone, Code: testCode, Password: testPass, Nickname: "n", Device: testDevice}); err != nil {
		t.Fatalf("code should still be valid: %v", err)
	}
}

func TestSmsCodeAttemptsExhausted(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.sms.Send(ctx, testPhone, PurposeRegister, ""); err != nil {
		t.Fatal(err)
	}
	in := RegisterInput{Phone: testPhone, Code: "000000", Password: testPass, Nickname: "n", Device: testDevice}
	for i := 0; i < 2; i++ {
		_, _, err := e.auth.Register(ctx, in)
		wantReason(t, err, v1.ErrorReason_SMS_CODE_INVALID)
	}
	_, _, err := e.auth.Register(ctx, in)
	wantReason(t, err, v1.ErrorReason_SMS_CODE_ATTEMPTS_EXCEEDED)
	// 作废之后正确的验证码也不能用
	in.Code = testCode
	_, _, err = e.auth.Register(ctx, in)
	wantReason(t, err, v1.ErrorReason_SMS_CODE_INVALID)
}

func TestSendSmsRules(t *testing.T) {
	ctx := context.Background()

	t.Run("login code requires registered phone", func(t *testing.T) {
		e := newEnv(t)
		_, err := e.sms.Send(ctx, testPhone, PurposeLogin, "")
		wantReason(t, err, v1.ErrorReason_USER_NOT_FOUND)
	})
	t.Run("ip rate limited", func(t *testing.T) {
		e := newEnv(t)
		e.limiter.deny = true
		_, err := e.sms.Send(ctx, testPhone, PurposeRegister, "")
		wantReason(t, err, v1.ErrorReason_RATE_LIMITED)
		if got := kerrors.FromError(err).Metadata["retry_after_seconds"]; got != "30" {
			t.Fatalf("retry_after_seconds = %q", got)
		}
	})
	t.Run("cooldown", func(t *testing.T) {
		e := newEnv(t)
		e.store.slot, e.store.retry = SlotTooFrequent, 42*time.Second
		_, err := e.sms.Send(ctx, testPhone, PurposeRegister, "")
		wantReason(t, err, v1.ErrorReason_SMS_SEND_TOO_FREQUENT)
		if got := kerrors.FromError(err).Metadata["retry_after_seconds"]; got != "42" {
			t.Fatalf("retry_after_seconds = %q", got)
		}
	})
	t.Run("daily limit resets at beijing midnight", func(t *testing.T) {
		e := newEnv(t)
		e.store.slot = SlotDailyLimit
		// 08:00 UTC 是北京时间 16:00，距离次日零点 8 小时
		_, err := e.sms.Send(ctx, testPhone, PurposeRegister, "")
		wantReason(t, err, v1.ErrorReason_SMS_DAILY_LIMIT_EXCEEDED)
		if got := kerrors.FromError(err).Metadata["retry_after_seconds"]; got != "28800" {
			t.Fatalf("retry_after_seconds = %q", got)
		}
	})
	t.Run("sender failure releases slot and code", func(t *testing.T) {
		e := newEnv(t)
		e.sender.err = errors.New("provider down")
		_, err := e.sms.Send(ctx, testPhone, PurposeRegister, "")
		wantReason(t, err, v1.ErrorReason_SMS_SEND_FAILED)
		if e.store.released != 1 || len(e.store.codes) != 0 {
			t.Fatalf("released=%d codes=%v", e.store.released, e.store.codes)
		}
	})
	t.Run("random code when no fixed code", func(t *testing.T) {
		e := newEnv(t)
		e.sms.fixedCode = ""
		if _, err := e.sms.Send(ctx, testPhone, PurposeRegister, ""); err != nil {
			t.Fatal(err)
		}
		if len(e.sender.lastCode) != 6 || e.store.codes[smsKey(PurposeRegister, testPhone)] != hashCode(e.sender.lastCode) {
			t.Fatalf("code %q not stored as hash", e.sender.lastCode)
		}
	})
}

func TestFixedCodeRequiresLogProvider(t *testing.T) {
	_, err := NewSmsUsecase(nil, nil, nil, nil, nil, &conf.Sms{Provider: "aliyun", FixedCode: "000000"}, nil)
	if err == nil {
		t.Fatal("expected error for fixed code with non-log provider")
	}
}

func TestLoginLockAfterFailures(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.register(t, testPhone)
	for i := 0; i < 3; i++ {
		_, _, err := e.auth.LoginByPassword(ctx, testPhone, "wrong1234", testDevice, "")
		wantReason(t, err, v1.ErrorReason_INVALID_CREDENTIALS)
	}
	// 锁定期间正确的密码也不能登录
	_, _, err := e.auth.LoginByPassword(ctx, testPhone, testPass, testDevice, "")
	wantReason(t, err, v1.ErrorReason_LOGIN_LOCKED)
}

func TestSmsLogin(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.register(t, testPhone)
	if _, err := e.sms.Send(ctx, testPhone, PurposeLogin, ""); err != nil {
		t.Fatal(err)
	}
	// 注册码与登录码隔离：登录码不能用于注册流程，反之亦然
	_, _, err := e.auth.Register(ctx, RegisterInput{Phone: "13800000002", Code: testCode, Password: testPass, Nickname: "n", Device: testDevice})
	wantReason(t, err, v1.ErrorReason_SMS_CODE_INVALID)
	if _, _, err := e.auth.LoginBySms(ctx, testPhone, testCode, testDevice, ""); err != nil {
		t.Fatalf("sms login: %v", err)
	}
}

func TestSameDeviceLoginReplacesSession(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u, first := e.register(t, testPhone)
	oldSID, _, _ := parseRefreshToken(first.RefreshToken)

	if _, _, err := e.auth.LoginByPassword(ctx, testPhone, testPass, testDevice, ""); err != nil {
		t.Fatal(err)
	}
	s, _ := e.sessions.FindByID(ctx, oldSID)
	if s.RevokedAt == nil || s.RevokeReason != ReasonReplaced {
		t.Fatalf("old session not replaced: %+v", s)
	}
	if len(e.revoker.sessions) != 1 || e.revoker.sessions[0] != oldSID {
		t.Fatalf("revocation list = %v", e.revoker.sessions)
	}
	ev, ok := e.events.msgs[len(e.events.msgs)-1].(*v1.SessionRevoked)
	if !ok || ev.GetSessionId() != oldSID || ev.GetUserId() != u.ID || ev.GetReason() != v1.RevokeReason_REVOKE_REASON_REPLACED {
		t.Fatalf("event = %v", e.events.msgs)
	}
	if list, _ := e.session.List(ctx, principalOf(u, first)); len(list) != 1 {
		t.Fatalf("active sessions = %d, want 1", len(list))
	}
}

func TestRefreshRotationAndReuseDetection(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, pair1 := e.register(t, testPhone)
	sid, _, _ := parseRefreshToken(pair1.RefreshToken)

	e.clock = e.clock.Add(time.Minute)
	pair2, err := e.auth.Refresh(ctx, pair1.RefreshToken, "")
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if pair2.RefreshToken == pair1.RefreshToken {
		t.Fatal("refresh token not rotated")
	}

	// 宽限期内重复使用旧令牌：并发刷新，不吊销
	e.clock = e.clock.Add(5 * time.Second)
	_, err = e.auth.Refresh(ctx, pair1.RefreshToken, "")
	wantReason(t, err, v1.ErrorReason_REFRESH_TOKEN_ROTATED)
	if s, _ := e.sessions.FindByID(ctx, sid); s.RevokedAt != nil {
		t.Fatal("session revoked within grace period")
	}

	// 宽限期之后再出现：判定为重放，吊销整个会话
	e.clock = e.clock.Add(time.Minute)
	_, err = e.auth.Refresh(ctx, pair1.RefreshToken, "")
	wantReason(t, err, v1.ErrorReason_REFRESH_TOKEN_INVALID)
	s, _ := e.sessions.FindByID(ctx, sid)
	if s.RevokedAt == nil || s.RevokeReason != ReasonRefreshReuse {
		t.Fatalf("session not revoked on reuse: %+v", s)
	}
	// 合法持有者的新令牌也随之失效
	_, err = e.auth.Refresh(ctx, pair2.RefreshToken, "")
	wantReason(t, err, v1.ErrorReason_REFRESH_TOKEN_INVALID)
}

func TestRefreshInvalidInputs(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, pair := e.register(t, testPhone)
	for _, tok := range []string{"", "garbage", "abc.def", "0.x", "999.secret", pair.RefreshToken + "x"} {
		_, err := e.auth.Refresh(ctx, tok, "")
		wantReason(t, err, v1.ErrorReason_REFRESH_TOKEN_INVALID)
	}
	// 过期
	e.clock = e.clock.Add(31 * 24 * time.Hour)
	_, err := e.auth.Refresh(ctx, pair.RefreshToken, "")
	wantReason(t, err, v1.ErrorReason_REFRESH_TOKEN_INVALID)
}

func TestLogoutAndKick(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u, web := e.register(t, testPhone)
	_, ios, err := e.auth.LoginByPassword(ctx, testPhone, testPass, Device{ID: "device-ios-1", Platform: 3}, "")
	if err != nil {
		t.Fatal(err)
	}
	other, otherPair := e.register(t, "13800000002")

	// 不能踢别人的设备，也不暴露会话是否存在
	err = e.session.Revoke(ctx, principalOf(other, otherPair), principalOf(u, ios).SessionID)
	wantReason(t, err, v1.ErrorReason_SESSION_NOT_FOUND)

	iosSID := principalOf(u, ios).SessionID
	if err := e.session.Revoke(ctx, principalOf(u, web), iosSID); err != nil {
		t.Fatalf("kick: %v", err)
	}
	err = e.session.Revoke(ctx, principalOf(u, web), iosSID)
	wantReason(t, err, v1.ErrorReason_SESSION_NOT_FOUND)
	_, err = e.auth.Refresh(ctx, ios.RefreshToken, "")
	wantReason(t, err, v1.ErrorReason_REFRESH_TOKEN_INVALID)

	if err := e.auth.Logout(ctx, principalOf(u, web)); err != nil {
		t.Fatal(err)
	}
	// 重复退出是幂等的
	if err := e.auth.Logout(ctx, principalOf(u, web)); err != nil {
		t.Fatal(err)
	}
	if got := e.revoker.sessions; len(got) != 2 {
		t.Fatalf("revoked sessions = %v, want kick + logout", got)
	}
}

func TestBanAndUnban(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	adminUser, adminPair := e.register(t, "13800000009")
	e.users.byID[adminUser.ID].Role = RoleAdmin
	adminUser.Role = RoleAdmin
	admin := principalOf(adminUser, adminPair)
	u, pair := e.register(t, testPhone)

	wantReason(t, e.admin.Ban(ctx, principalOf(u, pair), adminUser.ID), v1.ErrorReason_PERMISSION_DENIED)
	wantReason(t, e.admin.Ban(ctx, admin, admin.UserID), v1.ErrorReason_PERMISSION_DENIED)
	wantReason(t, e.admin.Ban(ctx, admin, 424242), v1.ErrorReason_USER_NOT_FOUND)

	if err := e.admin.Ban(ctx, admin, u.ID); err != nil {
		t.Fatalf("ban: %v", err)
	}
	if got, _ := e.users.FindByID(ctx, u.ID); got.Status != StatusBanned {
		t.Fatal("status not banned")
	}
	if _, ok := e.revoker.users[u.ID]; !ok {
		t.Fatal("user revocation not written")
	}
	if list, _ := e.sessions.ListActive(ctx, u.ID); len(list) != 0 {
		t.Fatalf("sessions still active: %d", len(list))
	}
	if _, ok := e.events.msgs[len(e.events.msgs)-1].(*v1.UserBanned); !ok {
		t.Fatalf("last event = %T, want UserBanned", e.events.msgs[len(e.events.msgs)-1])
	}
	// 重复封禁是幂等的
	if err := e.admin.Ban(ctx, admin, u.ID); err != nil {
		t.Fatalf("ban again: %v", err)
	}

	_, _, err := e.auth.LoginByPassword(ctx, testPhone, testPass, testDevice, "")
	wantReason(t, err, v1.ErrorReason_USER_BANNED)
	// 密码错误时不暴露封禁状态
	_, _, err = e.auth.LoginByPassword(ctx, testPhone, "wrong1234", testDevice, "")
	wantReason(t, err, v1.ErrorReason_INVALID_CREDENTIALS)
	_, err = e.auth.Refresh(ctx, pair.RefreshToken, "")
	wantReason(t, err, v1.ErrorReason_REFRESH_TOKEN_INVALID)

	if err := e.admin.Unban(ctx, admin, u.ID); err != nil {
		t.Fatalf("unban: %v", err)
	}
	e.guard.failures = map[string]int{}
	if _, _, err := e.auth.LoginByPassword(ctx, testPhone, testPass, testDevice, ""); err != nil {
		t.Fatalf("login after unban: %v", err)
	}
}

func TestUpdateMe(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u, _ := e.register(t, testPhone)

	future := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)
	_, err := e.user.UpdateMe(ctx, u.ID, ProfileUpdate{SetBirthday: true, Birthday: &future})
	if kerrors.Reason(err) != "VALIDATOR" {
		t.Fatalf("future birthday: %v", err)
	}
	// 北京时间已经是 11 月 18 日，当天出生是允许的
	today := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	name := "new"
	got, err := e.user.UpdateMe(ctx, u.ID, ProfileUpdate{Nickname: &name, SetBirthday: true, Birthday: &today})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.Nickname != "new" || got.Birthday == nil || len(e.users.evicted) != 1 {
		t.Fatalf("got %+v, evicted %v", got, e.users.evicted)
	}
	// 清除生日
	got, err = e.user.UpdateMe(ctx, u.ID, ProfileUpdate{SetBirthday: true})
	if err != nil || got.Birthday != nil {
		t.Fatalf("clear birthday: %+v, %v", got, err)
	}

	if _, err := ParseBirthday("2026-02-30"); kerrors.Reason(err) != "VALIDATOR" {
		t.Fatalf("invalid date accepted: %v", err)
	}
	_, err = e.user.GetUser(ctx, 424242)
	wantReason(t, err, v1.ErrorReason_USER_NOT_FOUND)
}
