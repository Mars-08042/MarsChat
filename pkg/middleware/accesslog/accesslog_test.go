package accesslog

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	kerrors "github.com/go-kratos/kratos/v3/errors"
	"github.com/go-kratos/kratos/v3/transport"

	v1 "github.com/Mars-08042/MarsChat/api/account/v1"
)

const secret = "secret-pass-123"

type fakeTransport struct{}

func (fakeTransport) Kind() transport.Kind            { return transport.KindHTTP }
func (fakeTransport) Endpoint() string                { return "" }
func (fakeTransport) Operation() string               { return v1.OperationAccountServiceLoginByPassword }
func (fakeTransport) RequestHeader() transport.Header { return header{} }
func (fakeTransport) ReplyHeader() transport.Header   { return header{} }

type header struct{}

func (header) Get(string) string      { return "" }
func (header) Set(string, string)     {}
func (header) Add(string, string)     {}
func (header) Keys() []string         { return nil }
func (header) Values(string) []string { return nil }

// call 用一个带密码的登录请求经过中间件，返回结果和日志输出。
func call(t *testing.T, handler func(context.Context, any) (any, error)) (any, error, string) {
	t.Helper()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	ctx := transport.NewServerContext(context.Background(), fakeTransport{})
	req := &v1.LoginByPasswordRequest{Phone: "13800000001", Password: secret}
	reply, err := Server(logger)(handler)(ctx, req)
	return reply, err, buf.String()
}

func TestSuccessLogsOperationWithoutRequestBody(t *testing.T) {
	reply, err, logs := call(t, func(context.Context, any) (any, error) { return "ok", nil })
	if err != nil || reply != "ok" {
		t.Fatalf("reply = %v, err = %v", reply, err)
	}
	if strings.Contains(logs, secret) || strings.Contains(logs, "13800000001") {
		t.Fatalf("request body leaked into log: %s", logs)
	}
	for _, want := range []string{"level=INFO", "operation=" + v1.OperationAccountServiceLoginByPassword, "code=200"} {
		if !strings.Contains(logs, want) {
			t.Errorf("log missing %q: %s", want, logs)
		}
	}
}

func TestBusinessErrorPassesThroughAtInfo(t *testing.T) {
	bizErr := kerrors.Unauthorized("INVALID_CREDENTIALS", "手机号或密码错误")
	_, err, logs := call(t, func(context.Context, any) (any, error) { return nil, bizErr })
	if !errors.Is(err, bizErr) {
		t.Fatalf("business error changed: %v", err)
	}
	if !strings.Contains(logs, "level=INFO") || !strings.Contains(logs, "reason=INVALID_CREDENTIALS") {
		t.Fatalf("business error should be logged at INFO with reason: %s", logs)
	}
}

func TestInternalErrorIsSanitized(t *testing.T) {
	dbErr := errors.New("Error 1062 (23000): Duplicate entry '13800000001' for key 'users.uk_phone'")
	_, err, logs := call(t, func(context.Context, any) (any, error) { return nil, dbErr })
	se := kerrors.FromError(err)
	if se.Code != 500 || se.Reason != "INTERNAL" || strings.Contains(se.Message, "1062") {
		t.Fatalf("internal error returned to client: %+v", se)
	}
	// 原始错误只进日志，方便排查
	if !strings.Contains(logs, "level=ERROR") || !strings.Contains(logs, "Duplicate entry") {
		t.Fatalf("original error not logged: %s", logs)
	}
	if strings.Contains(logs, secret) {
		t.Fatalf("request body leaked into log: %s", logs)
	}
}

func TestPanicRecoveredWithoutRequestBody(t *testing.T) {
	_, err, logs := call(t, func(context.Context, any) (any, error) { panic("boom") })
	if se := kerrors.FromError(err); se.Code != 500 || se.Reason != "INTERNAL" {
		t.Fatalf("panic not converted to internal error: %v", err)
	}
	if !strings.Contains(logs, "panic recovered") || !strings.Contains(logs, "boom") {
		t.Fatalf("panic not logged: %s", logs)
	}
	if strings.Contains(logs, secret) {
		t.Fatalf("request body leaked into panic log: %s", logs)
	}
}
