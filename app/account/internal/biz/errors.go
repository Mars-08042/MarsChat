package biz

import (
	"math"
	"strconv"
	"time"

	"github.com/go-kratos/kratos/v3/errors"

	v1 "github.com/Mars-08042/MarsChat/api/account/v1"
)

func newErr(code int, reason v1.ErrorReason, message string) *errors.Error {
	return errors.New(code, reason.String(), message)
}

// withRetryAfter 在 429 错误中附带需要等待的秒数，客户端据此显示倒计时。
func withRetryAfter(e *errors.Error, d time.Duration) *errors.Error {
	secs := int(math.Ceil(d.Seconds()))
	if secs < 1 {
		secs = 1
	}
	return e.WithMetadata(map[string]string{"retry_after_seconds": strconv.Itoa(secs)})
}

func ErrCodeTooFrequent(retry time.Duration) error {
	return withRetryAfter(newErr(429, v1.ErrorReason_CODE_SEND_TOO_FREQUENT, "验证码发送过于频繁，请稍后再试"), retry)
}

func ErrCodeDailyLimit(retry time.Duration) error {
	return withRetryAfter(newErr(429, v1.ErrorReason_CODE_DAILY_LIMIT_EXCEEDED, "今日验证码发送次数已达上限"), retry)
}

func ErrRateLimited(retry time.Duration) error {
	return withRetryAfter(newErr(429, v1.ErrorReason_RATE_LIMITED, "请求过于频繁，请稍后再试"), retry)
}

func ErrLoginLocked(retry time.Duration) error {
	return withRetryAfter(newErr(429, v1.ErrorReason_LOGIN_LOCKED, "密码错误次数过多，请稍后再试或使用验证码登录"), retry)
}

func ErrCodeSendFailed() error {
	return newErr(503, v1.ErrorReason_CODE_SEND_FAILED, "验证码发送失败，请重试")
}

func ErrDependencyUnavailable() error {
	return newErr(503, v1.ErrorReason_DEPENDENCY_UNAVAILABLE, "服务暂时不可用，请稍后重试")
}

func ErrCodeInvalid() error {
	return newErr(400, v1.ErrorReason_CODE_INVALID, "验证码错误或已过期")
}

func ErrCodeExhausted() error {
	return newErr(400, v1.ErrorReason_CODE_ATTEMPTS_EXCEEDED, "验证码错误次数过多，请重新获取")
}

func ErrWeakPassword() error {
	return newErr(400, v1.ErrorReason_WEAK_PASSWORD, "密码必须同时包含字母和数字")
}

func ErrInvalidBirthday() error {
	return errors.BadRequest("VALIDATOR", "birthday must be a valid past date in YYYY-MM-DD format")
}

func ErrInvalidCredentials() error {
	return newErr(401, v1.ErrorReason_INVALID_CREDENTIALS, "邮箱或密码错误")
}

func ErrRefreshTokenInvalid() error {
	return newErr(401, v1.ErrorReason_REFRESH_TOKEN_INVALID, "登录已失效，请重新登录")
}

func ErrUserBanned() error {
	return newErr(403, v1.ErrorReason_USER_BANNED, "账号已被封禁")
}

func ErrPermissionDenied() error {
	return newErr(403, v1.ErrorReason_PERMISSION_DENIED, "没有权限")
}

func ErrUserNotFound() error {
	return newErr(404, v1.ErrorReason_USER_NOT_FOUND, "用户不存在")
}

func ErrSessionNotFound() error {
	return newErr(404, v1.ErrorReason_SESSION_NOT_FOUND, "设备不存在或已下线")
}

func ErrEmailRegistered() error {
	return newErr(409, v1.ErrorReason_EMAIL_ALREADY_REGISTERED, "邮箱已注册")
}

func ErrRefreshTokenRotated() error {
	return newErr(409, v1.ErrorReason_REFRESH_TOKEN_ROTATED, "令牌已被刷新，请使用最新的令牌")
}
