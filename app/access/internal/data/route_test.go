package data

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/go-kratos/kratos/v3"

	"github.com/Mars-08042/MarsChat/app/access/internal/conf"
)

// fakeApp 模拟 Kratos 在 BeforeStart 钩子的上下文中提供的应用信息。
type fakeApp struct{ endpoints []string }

func (fakeApp) ID() string                  { return "access-1" }
func (fakeApp) Name() string                { return "marschat-access" }
func (fakeApp) Version() string             { return "test" }
func (fakeApp) Metadata() map[string]string { return nil }
func (a fakeApp) Endpoint() []string        { return a.endpoints }

func TestRouteRepoResolveAddr(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	withApp := func(endpoints ...string) context.Context {
		return kratos.NewContext(context.Background(), fakeApp{endpoints: endpoints})
	}

	tests := []struct {
		name       string
		configured string
		ctx        context.Context
		want       string
		wantErr    bool
	}{
		{
			name: "取注册的 gRPC 端点，忽略 HTTP 端点",
			ctx:  withApp("http://172.30.0.21:18000", "grpc://172.30.0.21:19100"),
			want: "172.30.0.21:19100",
		},
		{
			name:       "配置优先",
			configured: "10.0.0.5:29100",
			ctx:        withApp("http://172.30.0.21:18000", "grpc://172.30.0.21:19100"),
			want:       "10.0.0.5:29100",
		},
		{
			name:    "没有 gRPC 端点",
			ctx:     withApp("http://172.30.0.21:18000"),
			wantErr: true,
		},
		{
			name:    "上下文中没有应用信息",
			ctx:     context.Background(),
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRouteRepo(nil, &conf.Bootstrap{AdvertiseAddr: tt.configured}, logger)
			err := r.ResolveAddr(tt.ctx)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want error, got addr %q", r.addr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveAddr: %v", err)
			}
			if r.addr != tt.want {
				t.Errorf("addr = %q, want %q", r.addr, tt.want)
			}
		})
	}
}
