package biz

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math/big"
	"time"

	v1 "github.com/Mars-08042/MarsChat/api/account/v1"
	"github.com/Mars-08042/MarsChat/app/account/internal/conf"
)

// 每日上限按北京时间的自然日计算；用固定时区，不依赖系统的 tzdata
var dayZone = time.FixedZone("CST", 8*3600)

const (
	PurposeRegister = int32(v1.SmsPurpose_SMS_PURPOSE_REGISTER)
	PurposeLogin    = int32(v1.SmsPurpose_SMS_PURPOSE_LOGIN)
)

// SmsUsecase 负责发送和校验短信验证码。
type SmsUsecase struct {
	store     SmsCodeStore
	sender    SmsSender
	limiter   RateLimiter
	users     UserRepo
	policy    *PolicyHolder
	fixedCode string
	logger    *slog.Logger
	now       func() time.Time
}

// NewSmsUsecase 创建短信用例。固定验证码只允许和只写日志的发送方一起使用。
func NewSmsUsecase(store SmsCodeStore, sender SmsSender, limiter RateLimiter, users UserRepo, policy *PolicyHolder, c *conf.Sms, logger *slog.Logger) (*SmsUsecase, error) {
	if c.GetFixedCode() != "" && c.GetProvider() != "log" {
		return nil, fmt.Errorf("sms.fixed_code is only allowed with the log provider, got %q", c.GetProvider())
	}
	return &SmsUsecase{
		store: store, sender: sender, limiter: limiter, users: users, policy: policy,
		fixedCode: c.GetFixedCode(), logger: logger, now: nowUTC,
	}, nil
}

// Send 发送验证码，返回距离下次可以发送的时间。
func (uc *SmsUsecase) Send(ctx context.Context, phone string, purpose int32, ip string) (time.Duration, error) {
	p := uc.policy.Load().GetSms()

	// 先按 IP 限流，手机号是否注册的查询也计入，限制枚举速度。
	// Redis 故障时拒绝：短信按条计费，不能在失去防护时继续发送
	res, err := uc.limiter.Allow(ctx, "account:rl:sms_ip:"+ipKey(ip), int(p.GetIpLimit().GetLimit()), p.GetIpLimit().GetWindow().AsDuration())
	if err != nil {
		return 0, err
	}
	if !res.Allowed {
		return 0, ErrRateLimited(res.RetryAfter)
	}

	exists, err := uc.users.ExistsByPhone(ctx, phone)
	if err != nil {
		return 0, err
	}
	if purpose == PurposeRegister && exists {
		return 0, ErrPhoneRegistered()
	}
	if purpose == PurposeLogin && !exists {
		return 0, ErrUserNotFound()
	}

	interval := p.GetSendInterval().AsDuration()
	now := uc.now().In(dayZone)
	day := now.Format("20060102")
	slot, retry, err := uc.store.AcquireSendSlot(ctx, phone, day, interval, int(p.GetDailyLimitPerPhone()))
	if err != nil {
		return 0, err
	}
	switch slot {
	case SlotTooFrequent:
		return 0, ErrSmsTooFrequent(retry)
	case SlotDailyLimit:
		y, m, d := now.Date()
		return 0, ErrSmsDailyLimit(time.Date(y, m, d+1, 0, 0, 0, 0, dayZone).Sub(now))
	}

	code, err := uc.newCode()
	if err != nil {
		return 0, err
	}
	// 先保存再发送，保证用户收到短信时验证码已经可用
	if err := uc.store.SaveCode(ctx, purpose, phone, hashCode(code), p.GetCodeTtl().AsDuration()); err != nil {
		uc.release(ctx, phone, day)
		return 0, err
	}
	if err := uc.sender.Send(ctx, phone, code, purpose); err != nil {
		uc.logger.Error("send sms failed", slog.String("phone", MaskPhone(phone)), slog.Any("error", err))
		uc.release(ctx, phone, day)
		if err := uc.store.DeleteCode(ctx, purpose, phone); err != nil {
			uc.logger.Warn("delete unsent sms code failed", slog.String("phone", MaskPhone(phone)), slog.Any("error", err))
		}
		return 0, ErrSmsSendFailed()
	}
	return interval, nil
}

// release 撤销发送名额，让用户在短信平台失败后可以立即重试。
func (uc *SmsUsecase) release(ctx context.Context, phone, day string) {
	if err := uc.store.ReleaseSendSlot(ctx, phone, day); err != nil {
		uc.logger.Warn("release sms slot failed", slog.String("phone", MaskPhone(phone)), slog.Any("error", err))
	}
}

// Verify 校验并消耗验证码。
func (uc *SmsUsecase) Verify(ctx context.Context, purpose int32, phone, code string) error {
	p := uc.policy.Load().GetSms()
	res, err := uc.store.VerifyCode(ctx, purpose, phone, hashCode(code), int(p.GetMaxVerifyAttempts()))
	if err != nil {
		return err
	}
	switch res {
	case VerifyOK:
		return nil
	case VerifyExhausted:
		return ErrSmsCodeExhausted()
	default:
		return ErrSmsCodeInvalid()
	}
}

func (uc *SmsUsecase) newCode() (string, error) {
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

// MaskPhone 把手机号脱敏成 138****8000，用于日志。
func MaskPhone(phone string) string {
	if len(phone) != 11 {
		return "***"
	}
	return phone[:3] + "****" + phone[7:]
}

func ipKey(ip string) string {
	if ip == "" {
		return "unknown"
	}
	return ip
}
