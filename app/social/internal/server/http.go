package server

import (
	"log/slog"

	"github.com/go-kratos/kratos/v3/transport/http"

	v1 "github.com/Mars-08042/MarsChat/api/social/v1"
	"github.com/Mars-08042/MarsChat/app/social/internal/data"
	"github.com/Mars-08042/MarsChat/app/social/internal/service"
	"github.com/Mars-08042/MarsChat/pkg/bootstrap"
	"github.com/Mars-08042/MarsChat/pkg/conf"
	"github.com/Mars-08042/MarsChat/pkg/telemetry"
)

// NewHTTPServer 创建 HTTP 服务器，并挂载存活探针 /healthz、就绪探针 /readyz 和指标端点 /metrics。
// 就绪探针要检查存储连接，所以这里直接依赖 data 层，业务接口仍然经过 service 层。
// 探针不经过中间件，也就不需要令牌。
func NewHTTPServer(c *conf.Server, mws Middlewares, d *data.Data, social *service.SocialService, logger *slog.Logger) *http.Server {
	opts := []http.ServerOption{
		http.Middleware(mws...),
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
	// 指标端点同探针一样不经过中间件；网关只转发 /v1/... 与 /ws，外部访问不到
	srv.Handle("/metrics", telemetry.MetricsHandler())
	v1.RegisterSocialServiceHTTPServer(srv, social)
	return srv
}
