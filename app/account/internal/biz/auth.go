package biz

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "github.com/Mars-08042/MarsChat/api/account/v1"
	"github.com/Mars-08042/MarsChat/app/account/internal/conf"
	"github.com/Mars-08042/MarsChat/pkg/auth"
)

// 会话吊销原因，与 account.v1.RevokeReason 一致
const (
	ReasonLogout       = int32(v1.RevokeReason_REVOKE_REASON_LOGOUT)
	ReasonKicked       = int32(v1.RevokeReason_REVOKE_REASON_KICKED)
	ReasonBanned       = int32(v1.RevokeReason_REVOKE_REASON_BANNED)
	ReasonReplaced     = int32(v1.RevokeReason_REVOKE_REASON_REPLACED)
	ReasonRefreshReuse = int32(v1.RevokeReason_REVOKE_REASON_REFRESH_REUSE)
)

// RegisterInput 是注册参数。
type RegisterInput struct {
	Email    string
	Code     string
	Password string
	Nickname string
	Device   Device
	IP       string
}

// AuthUsecase 负责注册、登录、刷新令牌和退出。
type AuthUsecase struct {
	users      UserRepo
	sessions   SessionRepo
	events     EventRepo
	tx         Transaction
	tokens     *TokenService
	codes      *CodeUsecase
	limiter    RateLimiter
	guard      LoginGuard
	ids        IDGenerator
	revoker    Revoker
	policy     *PolicyHolder
	bcryptCost int
	dummyHash  string
	logger     *slog.Logger
	now        func() time.Time
}

// NewAuthUsecase 创建认证用例。
func NewAuthUsecase(users UserRepo, sessions SessionRepo, events EventRepo, tx Transaction, tokens *TokenService,
	codes *CodeUsecase, limiter RateLimiter, guard LoginGuard, ids IDGenerator, revoker Revoker,
	policy *PolicyHolder, sec *conf.Security, logger *slog.Logger) (*AuthUsecase, error) {
	cost := int(sec.GetBcryptCost())
	dummy, err := newDummyHash(cost)
	if err != nil {
		return nil, err
	}
	return &AuthUsecase{
		users: users, sessions: sessions, events: events, tx: tx, tokens: tokens, codes: codes,
		limiter: limiter, guard: guard, ids: ids, revoker: revoker, policy: policy,
		bcryptCost: cost, dummyHash: dummy, logger: logger,
		now: nowUTC,
	}, nil
}

// Register 校验验证码后创建用户，并直接登录当前设备。
func (uc *AuthUsecase) Register(ctx context.Context, in RegisterInput) (*User, *TokenPair, error) {
	// 密码强度不依赖外部状态，先检查，避免白白消耗验证码
	if err := checkPasswordStrength(in.Password); err != nil {
		return nil, nil, err
	}
	in.Email = NormalizeEmail(in.Email)
	// 发号同样在校验验证码之前：节点号租约失效时直接返回 503，验证码留着重试用
	uid, err := nextID(uc.ids)
	if err != nil {
		return nil, nil, err
	}
	if err := uc.codes.Verify(ctx, PurposeRegister, in.Email, in.Code); err != nil {
		return nil, nil, err
	}
	hash, err := hashPassword(in.Password, uc.bcryptCost)
	if err != nil {
		return nil, nil, err
	}
	now := uc.now()
	u := &User{
		ID: uid, Email: in.Email, PasswordHash: hash, Nickname: in.Nickname,
		Role: RoleUser, Status: StatusNormal, CreatedAt: now, UpdatedAt: now,
	}

	var pair *TokenPair
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		// 邮箱唯一约束兜底并发注册：两个请求都通过了验证码校验时，只有一个能插入成功
		if err := uc.users.Create(ctx, u); err != nil {
			if errors.Is(err, ErrEmailTaken) {
				return ErrEmailRegistered()
			}
			return err
		}
		var err error
		pair, _, err = uc.startSession(ctx, u, in.Device, in.IP, now)
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	uc.users.OnRegistered(ctx, u.ID)
	return u, pair, nil
}

// LoginByPassword 密码登录。
func (uc *AuthUsecase) LoginByPassword(ctx context.Context, email, password string, dev Device, ip string) (*User, *TokenPair, error) {
	email = NormalizeEmail(email)
	p := uc.policy.Load().GetLogin()
	if err := uc.checkIPLimit(ctx, ip); err != nil {
		return nil, nil, err
	}
	locked, retry, err := uc.guard.Locked(ctx, email, int(p.GetMaxFailures()))
	if err != nil {
		// 失败计数不可用时不阻断登录，bcrypt 本身的耗时和 IP 限流仍然限制了暴力破解速度
		uc.logger.Warn("login guard unavailable", slog.Any("error", err))
	} else if locked {
		return nil, nil, ErrLoginLocked(retry)
	}

	u, err := uc.users.FindByEmail(ctx, email)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, nil, err
	}
	if u == nil {
		comparePassword(uc.dummyHash, password)
		uc.recordFailure(ctx, email)
		return nil, nil, ErrInvalidCredentials()
	}
	if !comparePassword(u.PasswordHash, password) {
		uc.recordFailure(ctx, email)
		return nil, nil, ErrInvalidCredentials()
	}
	// 密码正确后才提示封禁，避免未认证的人探测账号状态
	if u.Status == StatusBanned {
		return nil, nil, ErrUserBanned()
	}
	if err := uc.guard.Reset(ctx, email); err != nil {
		uc.logger.Warn("reset login failures failed", slog.Any("error", err))
	}
	return uc.login(ctx, u, dev, ip)
}

// LoginByEmailCode 邮箱验证码登录。
func (uc *AuthUsecase) LoginByEmailCode(ctx context.Context, email, code string, dev Device, ip string) (*User, *TokenPair, error) {
	email = NormalizeEmail(email)
	if err := uc.checkIPLimit(ctx, ip); err != nil {
		return nil, nil, err
	}
	if err := uc.codes.Verify(ctx, PurposeLogin, email, code); err != nil {
		return nil, nil, err
	}
	u, err := uc.users.FindByEmail(ctx, email)
	if errors.Is(err, ErrNotFound) {
		return nil, nil, ErrUserNotFound()
	}
	if err != nil {
		return nil, nil, err
	}
	if u.Status == StatusBanned {
		return nil, nil, ErrUserBanned()
	}
	return uc.login(ctx, u, dev, ip)
}

func (uc *AuthUsecase) login(ctx context.Context, u *User, dev Device, ip string) (*User, *TokenPair, error) {
	now := uc.now()
	var (
		pair     *TokenPair
		replaced int64
	)
	err := uc.tx.InTx(ctx, func(ctx context.Context) error {
		// 锁住用户行再确认状态，与封禁的 UPDATE 互斥：要么封禁等本次登录提交后连同新会话一起吊销，
		// 要么本次登录等封禁提交后看到封禁状态，否则登录可能在封禁提交后插入一个有效会话。
		// 同一用户的并发登录也因此串行，避免同一设备并发登录在唯一索引上死锁
		status, err := uc.users.LockStatus(ctx, u.ID)
		if err != nil {
			return err
		}
		if status == StatusBanned {
			return ErrUserBanned()
		}
		pair, replaced, err = uc.startSession(ctx, u, dev, ip, now)
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	if replaced > 0 {
		revokeAfterCommit(ctx, uc.revoker, uc.tokens.RevocationTTL(), uc.logger, replaced)
	}
	return u, pair, nil
}

// startSession 在事务中吊销该设备的旧会话（同一设备只保留一个有效会话）并创建新会话。
// 返回令牌对和被替换的旧会话 ID。
func (uc *AuthUsecase) startSession(ctx context.Context, u *User, dev Device, ip string, now time.Time) (*TokenPair, int64, error) {
	replaced, err := uc.sessions.RevokeActiveByDevice(ctx, u.ID, dev.ID, ReasonReplaced, now)
	if err != nil {
		return nil, 0, err
	}
	if replaced > 0 {
		if err := addSessionRevoked(ctx, uc.events, u.ID, replaced, ReasonReplaced, now); err != nil {
			return nil, 0, err
		}
	}
	sid, err := nextID(uc.ids)
	if err != nil {
		return nil, 0, err
	}
	secret, hash, err := newRefreshSecret()
	if err != nil {
		return nil, 0, err
	}
	expires := now.Add(uc.tokens.refreshTTL)
	s := &Session{
		ID: sid, UserID: u.ID, Device: dev, RefreshHash: hash, RefreshExpiresAt: expires,
		LoginIP: ip, LastIP: ip, LastActiveAt: now, CreatedAt: now,
	}
	if err := uc.sessions.Create(ctx, s); err != nil {
		return nil, 0, err
	}
	pair, err := uc.tokens.issue(auth.Principal{UserID: u.ID, SessionID: sid, Role: u.Role}, secret, expires, now)
	if err != nil {
		return nil, 0, err
	}
	return pair, replaced, nil
}

// Refresh 用刷新令牌换新的令牌对，并检测重放。
func (uc *AuthUsecase) Refresh(ctx context.Context, token, ip string) (*TokenPair, error) {
	sid, hash, ok := parseRefreshToken(token)
	if !ok {
		return nil, ErrRefreshTokenInvalid()
	}
	s, err := uc.sessions.FindByID(ctx, sid)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrRefreshTokenInvalid()
	}
	if err != nil {
		return nil, err
	}
	now := uc.now()
	if s.RevokedAt != nil || !now.Before(s.RefreshExpiresAt) {
		return nil, ErrRefreshTokenInvalid()
	}

	switch {
	case hashEqual(hash, s.RefreshHash):
		return uc.rotate(ctx, s, hash, ip, now)
	case hashEqual(hash, s.PrevRefreshHash):
		if s.RotatedAt != nil && now.Sub(*s.RotatedAt) <= uc.tokens.rotationGrace {
			// 刚被同一客户端的另一个请求换掉（例如多个标签页同时刷新），不是攻击
			return nil, ErrRefreshTokenRotated()
		}
		// 已经换掉的令牌在宽限期之后又出现，说明令牌被复制过：吊销整个会话，合法用户也需要重新登录
		uc.logger.Warn("refresh token reuse detected, revoking session",
			slog.Int64("user_id", s.UserID), slog.Int64("session_id", s.ID))
		if err := uc.revokeSession(ctx, s.UserID, s.ID, ReasonRefreshReuse, now); err != nil {
			return nil, err
		}
		return nil, ErrRefreshTokenInvalid()
	default:
		return nil, ErrRefreshTokenInvalid()
	}
}

func (uc *AuthUsecase) rotate(ctx context.Context, s *Session, oldHash []byte, ip string, now time.Time) (*TokenPair, error) {
	u, err := uc.users.FindByID(ctx, s.UserID)
	if err != nil {
		return nil, err
	}
	if u.Status == StatusBanned {
		return nil, ErrUserBanned()
	}
	secret, newHash, err := newRefreshSecret()
	if err != nil {
		return nil, err
	}
	expires := now.Add(uc.tokens.refreshTTL)
	ok, err := uc.sessions.Rotate(ctx, s.ID, oldHash, newHash, expires, ip, now)
	if err != nil {
		return nil, err
	}
	if !ok {
		// 读取之后、更新之前被并发请求换掉了
		return nil, ErrRefreshTokenRotated()
	}
	// 角色从数据库重新读取，权限变化在下一次刷新时生效
	return uc.tokens.issue(auth.Principal{UserID: u.ID, SessionID: s.ID, Role: u.Role}, secret, expires, now)
}

// Logout 退出当前设备。重复退出是幂等的。
func (uc *AuthUsecase) Logout(ctx context.Context, p auth.Principal) error {
	return uc.revokeSession(ctx, p.UserID, p.SessionID, ReasonLogout, uc.now())
}

func (uc *AuthUsecase) revokeSession(ctx context.Context, userID, sid int64, reason int32, now time.Time) error {
	revoked, err := revokeSessionTx(ctx, uc.tx, uc.sessions, uc.events, userID, sid, reason, now)
	if err != nil {
		return err
	}
	if revoked {
		revokeAfterCommit(ctx, uc.revoker, uc.tokens.RevocationTTL(), uc.logger, sid)
	}
	return nil
}

func (uc *AuthUsecase) checkIPLimit(ctx context.Context, ip string) error {
	l := uc.policy.Load().GetLogin().GetIpLimit()
	res, err := uc.limiter.Allow(ctx, "account:rl:login_ip:"+ipKey(ip), int(l.GetLimit()), l.GetWindow().AsDuration())
	if err != nil {
		// 登录限流在 Redis 故障时放行，优先保证可用性
		uc.logger.Warn("login rate limiter unavailable", slog.Any("error", err))
		return nil
	}
	if !res.Allowed {
		return ErrRateLimited(res.RetryAfter)
	}
	return nil
}

func (uc *AuthUsecase) recordFailure(ctx context.Context, email string) {
	p := uc.policy.Load().GetLogin()
	if err := uc.guard.RecordFailure(ctx, email, int(p.GetMaxFailures()), p.GetFailureWindow().AsDuration(), p.GetLockDuration().AsDuration()); err != nil {
		uc.logger.Warn("record login failure failed", slog.Any("error", err))
	}
}

// revokeSessionTx 在事务中吊销会话并写入事件，返回是否真的吊销了（已吊销的会话返回 false）。
func revokeSessionTx(ctx context.Context, tx Transaction, sessions SessionRepo, events EventRepo, userID, sid int64, reason int32, now time.Time) (bool, error) {
	var revoked bool
	err := tx.InTx(ctx, func(ctx context.Context) error {
		ok, err := sessions.Revoke(ctx, sid, userID, reason, now)
		if err != nil || !ok {
			return err
		}
		revoked = true
		return addSessionRevoked(ctx, events, userID, sid, reason, now)
	})
	return revoked, err
}

func addSessionRevoked(ctx context.Context, events EventRepo, userID, sid int64, reason int32, now time.Time) error {
	return events.Add(ctx, TopicAccountEvents, strconv.FormatInt(userID, 10), &v1.SessionRevoked{
		UserId:    userID,
		SessionId: sid,
		Reason:    v1.RevokeReason(reason),
		RevokedAt: timestamppb.New(now),
	}, now)
}

// revokeAfterCommit 在事务提交后写入吊销名单。失败只记日志：刷新令牌已在数据库中失效，
// 访问令牌最多在有效期内继续可用。
func revokeAfterCommit(ctx context.Context, r Revoker, ttl time.Duration, logger *slog.Logger, sids ...int64) {
	if err := r.RevokeSessions(context.WithoutCancel(ctx), ttl, sids...); err != nil {
		logger.Error("write session revocation failed", slog.Any("session_ids", sids), slog.Any("error", err))
	}
}
