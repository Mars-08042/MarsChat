package retry

import (
	"context"
	"errors"
	"testing"
	"time"

	kerrors "github.com/go-kratos/kratos/v3/errors"
	"github.com/go-kratos/kratos/v3/middleware/circuitbreaker"
	"github.com/go-kratos/kratos/v3/selector"
	"github.com/go-kratos/kratos/v3/transport"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	messagev1 "github.com/Mars-08042/MarsChat/api/message/v1"
)

// fakeTransport 是客户端上下文中的 Transport，只用到 Operation。
type fakeTransport struct{ op string }

func (fakeTransport) Kind() transport.Kind            { return transport.KindGRPC }
func (fakeTransport) Endpoint() string                { return "discovery:///marschat-test" }
func (t fakeTransport) Operation() string             { return t.op }
func (fakeTransport) RequestHeader() transport.Header { return nil }
func (fakeTransport) ReplyHeader() transport.Header   { return nil }

const allowedOp = messagev1.MessageInternalService_SendMessage_FullMethodName

func clientCtx(t *testing.T, op string, timeout time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	t.Cleanup(cancel)
	return transport.NewClientContext(ctx, fakeTransport{op: op})
}

// handlerFailing 前 n 次返回 err，之后成功；返回处理器和调用计数。
func handlerFailing(n int, err error) (handler func(context.Context, any) (any, error), calls *int) {
	count := 0
	return func(context.Context, any) (any, error) {
		count++
		if count <= n {
			return nil, err
		}
		return "ok", nil
	}, &count
}

func TestRetryOnceOnUnavailable(t *testing.T) {
	h, calls := handlerFailing(1, status.Error(codes.Unavailable, "connection refused"))
	reply, err := Client(allowedOp)(h)(clientCtx(t, allowedOp, time.Second), nil)
	if err != nil || reply != "ok" {
		t.Fatalf("reply = %v, err = %v; want ok after one retry", reply, err)
	}
	if *calls != 2 {
		t.Errorf("calls = %d, want 2", *calls)
	}
}

func TestRetryAtMostOnce(t *testing.T) {
	h, calls := handlerFailing(5, status.Error(codes.Unavailable, "connection refused"))
	if _, err := Client(allowedOp)(h)(clientCtx(t, allowedOp, time.Second), nil); err == nil {
		t.Fatal("want the second failure")
	}
	if *calls != 2 {
		t.Errorf("calls = %d, want 2", *calls)
	}
}

func TestNoRetry(t *testing.T) {
	tests := []struct {
		name    string
		op      string
		timeout time.Duration
		err     error
	}{
		{"方法不在白名单", "/social.v1.SocialInternalService/Other", time.Second, status.Error(codes.Unavailable, "down")},
		{"业务错误", allowedOp, time.Second, status.Error(codes.InvalidArgument, "bad")},
		{"超时", allowedOp, time.Second, status.Error(codes.DeadlineExceeded, "timeout")},
		{"下游的依赖不可用", allowedOp, time.Second, kerrors.ServiceUnavailable("DEPENDENCY_UNAVAILABLE", "mysql down")},
		{"熔断打开", allowedOp, time.Second, circuitbreaker.ErrNotAllowed},
		{"没有可用实例", allowedOp, time.Second, selector.ErrNoAvailable},
		{"剩余时间不够", allowedOp, 50 * time.Millisecond, status.Error(codes.Unavailable, "down")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, calls := handlerFailing(1, tt.err)
			_, err := Client(allowedOp)(h)(clientCtx(t, tt.op, tt.timeout), nil)
			if !errors.Is(err, tt.err) {
				t.Fatalf("err = %v, want the original error %v", err, tt.err)
			}
			if *calls != 1 {
				t.Errorf("calls = %d, want 1 (no retry)", *calls)
			}
		})
	}
}
