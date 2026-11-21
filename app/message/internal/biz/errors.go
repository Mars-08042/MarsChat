package biz

import (
	"math"
	"strconv"
	"time"

	"github.com/go-kratos/kratos/v3/errors"

	v1 "github.com/Mars-08042/MarsChat/api/message/v1"
)

func newErr(code int, reason v1.ErrorReason, message string) *errors.Error {
	return errors.New(code, reason.String(), message)
}

// ErrConversationNotFound 同时表示会话不存在和当前用户不是成员，不让客户端借此探测会话 ID。
func ErrFileInvalid() error {
	return newErr(400, v1.ErrorReason_FILE_INVALID, "文件无效或无权使用")
}

func ErrConversationNotFound() error {
	return newErr(404, v1.ErrorReason_CONVERSATION_NOT_FOUND, "会话不存在")
}

// ErrRateLimited 附带需要等待的秒数，客户端据此提示稍后再发。
func ErrRateLimited(retryAfter time.Duration) error {
	secs := int(math.Ceil(retryAfter.Seconds()))
	if secs < 1 {
		secs = 1
	}
	return newErr(429, v1.ErrorReason_RATE_LIMITED, "发送过于频繁，请稍后再试").
		WithMetadata(map[string]string{"retry_after_seconds": strconv.Itoa(secs)})
}

func ErrDependencyUnavailable() error {
	return newErr(503, v1.ErrorReason_DEPENDENCY_UNAVAILABLE, "服务暂时不可用，请稍后重试")
}
