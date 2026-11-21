package biz

import (
	"math"
	"strconv"
	"time"

	"github.com/go-kratos/kratos/v3/errors"

	v1 "github.com/Mars-08042/MarsChat/api/media/v1"
)

func newErr(code int, reason v1.ErrorReason, message string) *errors.Error {
	return errors.New(code, reason.String(), message)
}

// ErrInvalidArgument 与 protovalidate 中间件的错误格式一致（400，reason 为 VALIDATOR），
// 用于 proto 规则表达不了的参数检查。
func ErrInvalidArgument(message string) error {
	return errors.BadRequest("VALIDATOR", message)
}

func ErrUploadIncomplete() *errors.Error {
	return newErr(400, v1.ErrorReason_UPLOAD_INCOMPLETE, "上传未完成，请重试")
}

func ErrDownloadURLInvalid() error {
	return newErr(403, v1.ErrorReason_DOWNLOAD_URL_INVALID, "下载地址无效或已过期")
}

func ErrFileNotFound() error {
	return newErr(404, v1.ErrorReason_FILE_NOT_FOUND, "文件不存在")
}

func ErrLengthRequired() error {
	return newErr(411, v1.ErrorReason_LENGTH_REQUIRED, "上传请求需要 Content-Length")
}

// ErrFileTooLarge 附带上限字节数，客户端据此提示。
func ErrFileTooLarge(maxBytes int64) error {
	return newErr(413, v1.ErrorReason_FILE_TOO_LARGE, "文件超过大小上限").
		WithMetadata(map[string]string{"max_bytes": strconv.FormatInt(maxBytes, 10)})
}

func ErrUnsupportedFileType() error {
	return newErr(415, v1.ErrorReason_UNSUPPORTED_FILE_TYPE, "头像必须是 JPEG、PNG、GIF 或 WebP 图片")
}

// ErrRateLimited 附带需要等待的秒数，客户端据此显示倒计时。
func ErrRateLimited(d time.Duration) error {
	secs := int(math.Ceil(d.Seconds()))
	if secs < 1 {
		secs = 1
	}
	return newErr(429, v1.ErrorReason_RATE_LIMITED, "上传过于频繁，请稍后再试").
		WithMetadata(map[string]string{"retry_after_seconds": strconv.Itoa(secs)})
}

func ErrDependencyUnavailable() *errors.Error {
	return newErr(503, v1.ErrorReason_DEPENDENCY_UNAVAILABLE, "服务暂时不可用，请稍后重试")
}
