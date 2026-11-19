package server

import (
	"log/slog"

	"github.com/go-kratos/kratos/v3/middleware/recovery"
	"github.com/go-kratos/kratos/v3/transport/http"

	"github.com/Mars-08042/MarsChat/app/social/internal/data"
	"github.com/Mars-08042/MarsChat/pkg/bootstrap"
	"github.com/Mars-08042/MarsChat/pkg/conf"
)

// NewHTTPServer 创建 HTTP 服务器，并挂载存活探针 /healthz 和就绪探针 /readyz。
// 就绪探针要检查存储连接，所以这里直接依赖 data 层，业务接口仍然经过 service 层。
func NewHTTPServer(c *conf.Server, d *data.Data, logger *slog.Logger) *http.Server {
	opts := []http.ServerOption{
		http.Middleware(recovery.Recovery()),
	}
	if addr := c.GetHttp().GetAddr(); addr != "" {
		opts = append(opts, http.Address(addr))
	}
	if t := c.GetHttp().GetTimeout(); t != nil {
		opts = append(opts, http.Timeout(t.AsDuration()))
	}
	srv := http.NewServer(opts...)
	srv.HandleFunc("/healthz", bootstrap.LivenessHandler)
	srv.Handle("/readyz", bootstrap.ReadinessHandler(logger, d.ReadinessChecks()...))
	return srv
}
