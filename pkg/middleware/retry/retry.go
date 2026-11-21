// Package retry 是 Kratos 客户端中间件：对白名单中的幂等方法，在连接级失败时再试一次。
//
// 只在“请求很可能没有被处理、换个实例就能成功”时重试：错误码为 503 且不带 Kratos 错误原因，
// 也就是 gRPC 层的 Unavailable（实例已退出、连接被拒绝、选中了已失效的连接）。以下情况不重试：
//   - 业务错误，以及下游自己返回的 DEPENDENCY_UNAVAILABLE：下游的依赖不可用时，换实例重试只会放大压力；
//   - 熔断打开（CIRCUITBREAKER）、没有可用实例（no_available_node）：这两个都带错误原因；
//   - 超时（504）：剩余时间已经用完；
//   - 剩余截止时间不够完成一次调用。
//
// grpc-go 已经透明重试“请求还没发出去”的失败，这里补上实例在处理途中退出的情况。
// 重试会再经过负载均衡选择。Kratos 的 gRPC 负载均衡只在 READY 状态的连接中选择节点，
// 实例退出后它的连接很快离开 READY，两次尝试之间的退避也给了连接状态更新的时间，所以重试通常落到其他实例；
// 连接状态还没更新时可能再次选中它，这时第二次同样失败，返回第二次的错误。
// 与熔断一起使用时把重试放在外层，每次尝试都计入熔断统计。
package retry

import (
	"context"
	"math/rand/v2"
	"net/http"
	"time"

	kerrors "github.com/go-kratos/kratos/v3/errors"
	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/transport"
)

const (
	// minRemaining 是重试前要求的剩余截止时间，不够时直接返回第一次的错误
	minRemaining = 100 * time.Millisecond
	// 两次尝试之间等待的随机区间，避免大量调用方同时重试
	minBackoff = 20 * time.Millisecond
	maxBackoff = 50 * time.Millisecond
)

// Client 返回客户端重试中间件。operations 是允许重试的方法全名（gRPC 生成代码中的 *_FullMethodName），
// 只应列出幂等的方法；不在其中的方法失败时原样返回。
func Client(operations ...string) middleware.Middleware {
	allowed := make(map[string]struct{}, len(operations))
	for _, op := range operations {
		allowed[op] = struct{}{}
	}
	return func(handler middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (any, error) {
			reply, err := handler(ctx, req)
			if err == nil || !Retryable(err) {
				return reply, err
			}
			info, ok := transport.FromClientContext(ctx)
			if !ok {
				return reply, err
			}
			if _, ok := allowed[info.Operation()]; !ok {
				return reply, err
			}
			if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < minRemaining+maxBackoff {
				return reply, err
			}
			backoff := minBackoff + rand.N(maxBackoff-minBackoff)
			select {
			case <-ctx.Done():
				return reply, err
			case <-time.After(backoff):
			}
			return handler(ctx, req)
		}
	}
}

// Retryable 判断错误是否为可以换实例重试的连接级失败。
func Retryable(err error) bool {
	e := kerrors.FromError(err)
	return e.Code == http.StatusServiceUnavailable && e.Reason == kerrors.UnknownReason
}
