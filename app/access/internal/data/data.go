// Package data 实现 biz 层定义的接口：令牌校验、Redis 在线路由，以及对 message 的调用。
package data

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/go-kratos/kratos/contrib/registry/consul/v3"
	kerrors "github.com/go-kratos/kratos/v3/errors"
	kgrpc "github.com/go-kratos/kratos/v3/transport/grpc"
	"github.com/google/wire"
	"github.com/redis/go-redis/v9"

	messagev1 "github.com/Mars-08042/MarsChat/api/message/v1"
	"github.com/Mars-08042/MarsChat/app/access/internal/biz"
	"github.com/Mars-08042/MarsChat/app/access/internal/conf"
	"github.com/Mars-08042/MarsChat/pkg/auth"
	"github.com/Mars-08042/MarsChat/pkg/bootstrap"
	pkgconf "github.com/Mars-08042/MarsChat/pkg/conf"
	"github.com/Mars-08042/MarsChat/pkg/route"
)

// ProviderSet 是 data 层的依赖提供者。
var ProviderSet = wire.NewSet(
	bootstrap.NewRedis,
	NewData,
	NewAuthenticator,
	NewRouteRepo,
	NewMessageSender,
)

// Data 持有共用的存储客户端。
type Data struct {
	rdb *redis.Client
}

// NewData 组装存储客户端，连接的关闭由各自的 provider 负责。
func NewData(rdb *redis.Client) *Data {
	return &Data{rdb: rdb}
}

// ReadinessChecks 返回就绪探针要检查的依赖。message 不在其中：它不可用时连接仍能维持和接收推送，
// 发消息返回 503，不应因此摘掉 access 的流量。
func (d *Data) ReadinessChecks() []bootstrap.Check {
	return []bootstrap.Check{bootstrap.RedisCheck(d.rdb)}
}

// NewAuthenticator 用与 HTTP 接口相同的规则校验首帧中的令牌。
func NewAuthenticator(c *pkgconf.Auth, rdb *redis.Client, logger *slog.Logger) (biz.Authenticator, error) {
	return auth.NewVerifier(c, rdb, logger)
}

type routeRepo struct {
	rdb  *redis.Client
	addr string
}

// NewRouteRepo 创建在线路由仓库，路由中记录本实例的 advertise_addr。
func NewRouteRepo(rdb *redis.Client, bc *conf.Bootstrap) biz.RouteRepo {
	return &routeRepo{rdb: rdb, addr: bc.GetAdvertiseAddr()}
}

func (r *routeRepo) Register(ctx context.Context, userID, connID, sessionID int64, ttl time.Duration) error {
	return route.Register(ctx, r.rdb, userID, route.Entry{ConnID: connID, SessionID: sessionID, Addr: r.addr}, ttl)
}

func (r *routeRepo) Unregister(ctx context.Context, userID, connID int64) error {
	return route.Unregister(ctx, r.rdb, userID, connID)
}

type messageSender struct {
	c messagev1.MessageInternalServiceClient
}

// NewMessageSender 创建 message 客户端，默认地址 discovery:///marschat-message。
// 连接是惰性的，message 暂时不可用不影响启动；超时同时也是服务发现首次解析的等待上限。
func NewMessageSender(bc *conf.Bootstrap, r *consul.Registry, logger *slog.Logger) (biz.MessageSender, func(), error) {
	c := bc.GetMessage()
	conn, err := kgrpc.NewClient(context.Background(),
		kgrpc.WithEndpoint(c.GetEndpoint()),
		kgrpc.WithDiscovery(r),
		kgrpc.WithTimeout(c.GetTimeout().AsDuration()),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("create message client %s: %w", c.GetEndpoint(), err)
	}
	cleanup := func() {
		if err := conn.Close(); err != nil {
			logger.Warn("close client failed", slog.String("upstream", "message"), slog.Any("error", err))
		}
	}
	return &messageSender{c: messagev1.NewMessageInternalServiceClient(conn)}, cleanup, nil
}

// Send 区分两类错误：message 返回的业务错误（带错误原因）原样返回，客户端看到具体原因；
// 超时、连接失败等没有错误原因的错误统一为 503 UPSTREAM_UNAVAILABLE。
func (s *messageSender) Send(ctx context.Context, p auth.Principal, connID int64, m *messagev1.OutgoingMessage) (*messagev1.SendResult, error) {
	resp, err := s.c.SendMessage(ctx, &messagev1.SendMessageRequest{
		SenderId:     p.UserID,
		SessionId:    p.SessionID,
		ConnectionId: connID,
		Message:      m,
	})
	if err != nil {
		if e := kerrors.FromError(err); e.Reason != "" {
			return nil, e
		}
		return nil, biz.ErrUpstreamUnavailable(err)
	}
	return resp.GetResult(), nil
}
