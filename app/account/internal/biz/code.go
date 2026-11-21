package biz

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"time"

	v1 "github.com/Mars-08042/MarsChat/api/account/v1"
	"github.com/Mars-08042/MarsChat/app/account/internal/conf"
)

// 每日上限按北京时间的自然日计算；用固定时区，不依赖系统的 tzdata
var dayZone = time.FixedZone("CST", 8*3600)

const (
	PurposeRegister = int32(v1.CodePurpose_CODE_PURPOSE_REGISTER)
	PurposeLogin    = int32(v1.CodePurpose_CODE_PURPOSE_LOGIN)
)

// CodeUsecase 负责发送和校验邮箱验证码。
type CodeUsecase struct {
	store     CodeStore
	sender    CodeSender
	limiter   RateLimiter
	users     UserRepo
	policy    *PolicyHolder
	fixedCode string
	logger    *slog.Logger
	now       func() time.Time
}

// NewCodeUsecase 创建验证码用例。固定验证码只允许和只写日志的发送方一起使用。
func NewCodeUsecase(store CodeStore, sender CodeSender, limiter RateLimiter, users UserRepo, policy *PolicyHolder, c *conf.Mail, logger *slog.Logger) (*CodeUsecase, error) {
	if c.GetFixedCode() != "" && c.GetProvider() != "log" {
		return nil, fmt.Errorf("mail.fixed_code is only allowed with the log provider, got %q", c.GetProvider())
	}
	return &CodeUsecase{
		store: store, sender: sender, limiter: limiter, users: users, policy: policy,
		fixedCode: c.GetFixedCode(), logger: logger, now: nowUTC,
	}, nil
}

// Send 发送验证码，返回距离下次可以发送的时间。
func (uc *CodeUsecase) Send(ctx context.Context, email string, purpose int32, ip string) (time.Duration, error) {
	email = NormalizeEmail(email)
	p := uc.policy.Load().GetEmailCode()

	// 先按 IP 限流，邮箱是否注册的查询也计入，限制枚举速度。
	// Redis 故障时拒绝：个人邮箱有每日发信上限，失去防护时被刷爆会让正常用户也收不到验证码
	res, err := uc.limiter.Allow(ctx, "account:rl:email_code_ip:"+ipKey(ip), int(p.GetIpLimit().GetLimit()), p.GetIpLimit().GetWindow().AsDuration())
	if err != nil {
		return 0, err
	}
	if !res.Allowed {
		return 0, ErrRateLimited(res.RetryAfter)
	}

	exists, err := uc.users.ExistsByEmail(ctx, email)
	if err != nil {
		return 0, err
	}
	if purpose == PurposeRegister && exists {
		return 0, ErrEmailRegistered()
	}
	if purpose == PurposeLogin && !exists {
		return 0, ErrUserNotFound()
	}

	interval := p.GetSendInterval().AsDuration()
	now := uc.now().In(dayZone)
	day := now.Format("20060102")
	slot, retry, err := uc.store.AcquireSendSlot(ctx, email, day, interval, int(p.GetDailyLimitPerEmail()))
	if err != nil {
		return 0, err
	}
	switch slot {
	case SlotTooFrequent:
		return 0, ErrCodeTooFrequent(retry)
	case SlotDailyLimit:
		y, m, d := now.Date()
		return 0, ErrCodeDailyLimit(time.Date(y, m, d+1, 0, 0, 0, 0, dayZone).Sub(now))
	}

	code, err := uc.newCode()
	if err != nil {
		return 0, err
	}
	// 先保存再发送，保证用户收到邮件时验证码已经可用
	if err := uc.store.SaveCode(ctx, purpose, email, hashCode(code), p.GetCodeTtl().AsDuration()); err != nil {
		uc.release(ctx, email, day)
		return 0, err
	}
	if err := uc.sender.Send(ctx, email, code, purpose, p.GetCodeTtl().AsDuration()); err != nil {
		uc.logger.Error("send verification code failed", slog.String("email", MaskEmail(email)), slog.Any("error", err))
		uc.release(ctx, email, day)
		if err := uc.store.DeleteCode(ctx, purpose, email); err != nil {
			uc.logger.Warn("delete unsent verification code failed", slog.String("email", MaskEmail(email)), slog.Any("error", err))
		}
		return 0, ErrCodeSendFailed()
	}
	return interval, nil
}

// release 撤销发送名额，让用户在邮件发送失败后可以立即重试。
func (uc *CodeUsecase) release(ctx context.Context, email, day string) {
	if err := uc.store.ReleaseSendSlot(ctx, email, day); err != nil {
		uc.logger.Warn("release verification code slot failed", slog.String("email", MaskEmail(email)), slog.Any("error", err))
	}
}

// Verify 校验并消耗验证码。
func (uc *CodeUsecase) Verify(ctx context.Context, purpose int32, email, code string) error {
	p := uc.policy.Load().GetEmailCode()
	res, err := uc.store.VerifyCode(ctx, purpose, email, hashCode(code), int(p.GetMaxVerifyAttempts()))
	if err != nil {
		return err
	}
	switch res {
	case VerifyOK:
		return nil
	case VerifyExhausted:
		return ErrCodeExhausted()
	default:
		return ErrCodeInvalid()
	}
}

func (uc *CodeUsecase) newCode() (string, error) {
	if uc.fixedCode != "" {
		return uc.fixedCode, nil
	}
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// hashCode 让 Redis 中不出现验证码明文。
func hashCode(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

// NormalizeEmail 去掉首尾空白并整体转为小写。存储、Redis 键和唯一性都按规范化后的值，
// 同一个邮箱换了大小写也只对应一个账号、一组发送计数。
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// MaskEmail 把邮箱脱敏成 a***@example.com，用于日志：只保留本地部分的第一个字符和域名。
func MaskEmail(email string) string {
	at := strings.LastIndexByte(email, '@')
	if at <= 0 || at == len(email)-1 {
		return "***"
	}
	return email[:1] + "***" + email[at:]
}

func ipKey(ip string) string {
	if ip == "" {
		return "unknown"
	}
	return ip
}
