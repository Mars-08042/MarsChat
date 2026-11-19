// Package server 创建 access 的 HTTP 服务器（WebSocket 与健康检查）和 gRPC 服务器（内部接口）。
package server

import (
	"log/slog"
	nethttp "net/http"
	"time"

	"github.com/go-kratos/kratos/contrib/middleware/validate/v3"
	"github.com/go-kratos/kratos/v3/transport/grpc"
	"github.com/go-kratos/kratos/v3/transport/http"
	"github.com/google/wire"
	"github.com/gorilla/websocket"

	v1 "github.com/Mars-08042/MarsChat/api/access/v1"
	"github.com/Mars-08042/MarsChat/app/access/internal/biz"
	"github.com/Mars-08042/MarsChat/app/access/internal/data"
	"github.com/Mars-08042/MarsChat/app/access/internal/service"
	"github.com/Mars-08042/MarsChat/pkg/bootstrap"
	"github.com/Mars-08042/MarsChat/pkg/conf"
	"github.com/Mars-08042/MarsChat/pkg/middleware/accesslog"
)

// ProviderSet 是 server 层的依赖提供者。
var ProviderSet = wire.NewSet(NewGRPCServer, NewHTTPServer)

// NewHTTPServer 创建 HTTP 服务器：WebSocket 入口 /ws，以及存活探针 /healthz 和就绪探针 /readyz。
// 没有 proto 定义的 HTTP 接口，所以不挂中间件；WebSocket 不经过 Kratos 的中间件和请求超时。
func NewHTTPServer(c *conf.Server, d *data.Data, hub *biz.Hub, logger *slog.Logger) *http.Server {
	var opts []http.ServerOption
	if addr := c.GetHttp().GetAddr(); addr != "" {
		opts = append(opts, http.Address(addr))
	}
	if t := c.GetHttp().GetTimeout(); t != nil {
		opts = append(opts, http.Timeout(t.AsDuration()))
	}
	srv := http.NewServer(opts...)
	srv.HandleFunc("/healthz", bootstrap.LivenessHandler)
	srv.Handle("/readyz", bootstrap.ReadinessHandler(logger, d.ReadinessChecks()...))
	srv.Handle("/ws", NewWebSocketHandler(hub))
	return srv
}

// NewGRPCServer 创建 gRPC 服务器，只提供内部接口（Push、Kick），不需要登录（服务间认证见 TD-26）。
func NewGRPCServer(c *conf.Server, internal *service.InternalService, logger *slog.Logger) *grpc.Server {
	opts := []grpc.ServerOption{
		grpc.Middleware(accesslog.Server(logger), validate.ProtoValidate()),
	}
	if addr := c.GetGrpc().GetAddr(); addr != "" {
		opts = append(opts, grpc.Address(addr))
	}
	if t := c.GetGrpc().GetTimeout(); t != nil {
		opts = append(opts, grpc.Timeout(t.AsDuration()))
	}
	srv := grpc.NewServer(opts...)
	v1.RegisterAccessInternalServiceServer(srv, internal)
	return srv
}

// NewWebSocketHandler 完成 WebSocket 握手后把连接交给 Hub。
func NewWebSocketHandler(hub *biz.Hub) nethttp.Handler {
	upgrader := websocket.Upgrader{
		HandshakeTimeout: 10 * time.Second,
		Subprotocols:     biz.Subprotocols,
		// 不校验 Origin：认证靠首帧中的令牌而不是 Cookie，跨站页面拿不到令牌，不存在跨站劫持 WebSocket 的问题
		CheckOrigin: func(*nethttp.Request) bool { return true },
	}
	return nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			// Upgrade 已经回复了 4xx
			return
		}
		hub.Serve(ws)
	})
}
