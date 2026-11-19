// Package accesslog 是服务端的访问日志、panic 恢复与错误脱敏中间件，替代 Kratos 自带的
// logging 与 recovery：
//   - Kratos logging 会把整个请求体写进日志，登录、注册请求里的密码、验证码、刷新令牌都会落盘；
//     它还把所有业务错误（如密码错误）记成 ERROR，告警没法用。这里只记操作名、状态码、原因和耗时，
//     5xx 记 ERROR，其余记 INFO。
//   - Kratos recovery 记录 panic 时同样会打印请求体。
//   - 非 Kratos 错误（数据库、Redis 等内部错误）原样返回会把内部信息暴露给客户端，这里记完日志后
//     统一换成不含细节的 500。
package accesslog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"time"

	kerrors "github.com/go-kratos/kratos/v3/errors"
	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/transport"
)

// ErrInternal 是返回给客户端的内部错误，不包含任何细节。
var ErrInternal = kerrors.InternalServer("INTERNAL", "服务器内部错误")

// Server 返回服务端中间件，应放在中间件链的最外层。
func Server(logger *slog.Logger) middleware.Middleware {
	return func(handler middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (reply any, err error) {
			start := time.Now()
			var kind, operation string
			if info, ok := transport.FromServerContext(ctx); ok {
				kind = info.Kind().String()
				operation = info.Operation()
			}
			defer func() {
				if r := recover(); r != nil {
					buf := make([]byte, 64<<10)
					buf = buf[:runtime.Stack(buf, false)]
					logger.ErrorContext(ctx, "panic recovered",
						slog.String("operation", operation),
						slog.String("panic", fmt.Sprint(r)),
						slog.String("stack", string(buf)))
					reply, err = nil, ErrInternal
				}
				var internal error
				if err != nil {
					var ke *kerrors.Error
					if !errors.As(err, &ke) {
						internal, err = err, ErrInternal
					}
				}
				log(ctx, logger, kind, operation, start, err, internal)
			}()
			return handler(ctx, req)
		}
	}
}

func log(ctx context.Context, logger *slog.Logger, kind, operation string, start time.Time, err, internal error) {
	code, reason := int32(200), ""
	if se := kerrors.FromError(err); se != nil {
		code, reason = se.Code, se.Reason
	}
	attrs := []slog.Attr{
		slog.String("component", kind),
		slog.String("operation", operation),
		slog.Int("code", int(code)),
		slog.String("reason", reason),
		slog.Duration("latency", time.Since(start)),
	}
	level := slog.LevelInfo
	if code >= 500 {
		level = slog.LevelError
	}
	if internal != nil {
		attrs = append(attrs, slog.Any("error", internal))
	} else if err != nil && code >= 500 {
		attrs = append(attrs, slog.Any("error", err))
	}
	logger.LogAttrs(ctx, level, "server request", attrs...)
}
