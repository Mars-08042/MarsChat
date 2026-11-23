// Package data 实现 biz 层定义的接口：令牌校验、Redis 在线路由与通话状态，对 message、social 的调用，
// 以及转发通话信令用的 access 直连客户端。
package data

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"github.com/go-kratos/kratos/contrib/registry/consul/v3"
	"github.com/go-kratos/kratos/v3"
	kerrors "github.com/go-kratos/kratos/v3/errors"
	"github.com/go-kratos/kratos/v3/middleware/circuitbreaker"
	kgrpc "github.com/go-kratos/kratos/v3/transport/grpc"
	"github.com/google/wire"
	"github.com/redis/go-redis/v9"

	messagev1 "github.com/Mars-08042/MarsChat/api/message/v1"
	socialv1 "github.com/Mars-08042/MarsChat/api/social/v1"
	"github.com/Mars-08042/MarsChat/app/access/internal/biz"
	"github.com/Mars-08042/MarsChat/app/access/internal/conf"
	"github.com/Mars-08042/MarsChat/pkg/accessclient"
	"github.com/Mars-08042/MarsChat/pkg/auth"
	"github.com/Mars-08042/MarsChat/pkg/bootstrap"
	pkgconf "github.com/Mars-08042/MarsChat/pkg/conf"
	"github.com/Mars-08042/MarsChat/pkg/middleware/retry"
	"github.com/Mars-08042/MarsChat/pkg/route"
	"github.com/Mars-08042/MarsChat/pkg/telemetry"
)

// ProviderSet 是 data 层的依赖提供者。
var ProviderSet = wire.NewSet(
	bootstrap.NewRedis,
	NewData,
	NewAuthenticator,
	NewRouteRepo,
	wire.Bind(new(biz.RouteRepo), new(*RouteRepo)),
	NewMessageClient,
	wire.Bind(new(biz.MessageSender), new(*MessageClient)),
	wire.Bind(new(biz.CallRecorder), new(*MessageClient)),
	NewCallRepo,
	NewCallPermission,
	NewForwarder,
)

// Data 持有共用的存储客户端。
type Data struct {
	rdb *redis.Client
}

// NewData 组装存储客户端，连接的关闭由各自的 provider 负责。
func NewData(rdb *redis.Client) *Data {
	return &Data{rdb: rdb}
}

// ReadinessChecks 返回就绪探针要检查的依赖。message、social 不在其中：它们不可用时连接仍能维持和接收推送，
// 发消息、发起呼叫返回 503，不应因此摘掉 access 的流量。
func (d *Data) ReadinessChecks() []bootstrap.Check {
	return []bootstrap.Check{bootstrap.RedisCheck(d.rdb)}
}

// NewAuthenticator 用与 HTTP 接口相同的规则校验首帧中的令牌。
func NewAuthenticator(c *pkgconf.Auth, rdb *redis.Client, logger *slog.Logger) (biz.Authenticator, error) {
	return auth.NewVerifier(c, rdb, logger)
}

// RouteRepo 读写在线路由，路由中记录本实例供 delivery 直连的 gRPC 地址。
type RouteRepo struct {
	rdb    *redis.Client
	addr   string
	logger *slog.Logger
}

// NewRouteRepo 创建在线路由仓库。配置了 advertise_addr 时直接使用，否则由 ResolveAddr 在启动时确定。
func NewRouteRepo(rdb *redis.Client, bc *conf.Bootstrap, logger *slog.Logger) *RouteRepo {
	return &RouteRepo{rdb: rdb, addr: bc.GetAdvertiseAddr(), logger: logger}
}

// ResolveAddr 在没有配置 advertise_addr 时，取 Kratos 注册到 Consul 的 gRPC 端点作为路由地址。
// 作为 kratos.BeforeStart 钩子调用：Kratos 在钩子之前已算好端点，HTTP 服务器在钩子之后才开始接受连接，
// 所以第一条连接登记路由时地址已经确定，之后只读不写，不需要加锁。
// 从端点取而不是在构造时依赖 gRPC 服务器，是因为 gRPC 服务器依赖 Hub、Hub 依赖本仓库，wire 会成环。
func (r *RouteRepo) ResolveAddr(ctx context.Context) error {
	if r.addr == "" {
		app, ok := kratos.FromContext(ctx)
		if !ok {
			return fmt.Errorf("resolve route address: no kratos app in context")
		}
		for _, e := range app.Endpoint() {
			if u, err := url.Parse(e); err == nil && u.Scheme == "grpc" {
				r.addr = u.Host
				break
			}
		}
		if r.addr == "" {
			return fmt.Errorf("resolve route address: no grpc endpoint in %v", app.Endpoint())
		}
	}
	r.logger.Info("route address resolved", slog.String("addr", r.addr))
	return nil
}

func (r *RouteRepo) Register(ctx context.Context, userID, connID, sessionID int64, ttl time.Duration) error {
	return route.Register(ctx, r.rdb, userID, route.Entry{ConnID: connID, SessionID: sessionID, Addr: r.addr}, ttl)
}

func (r *RouteRepo) Unregister(ctx context.Context, userID, connID int64) error {
	return route.Unregister(ctx, r.rdb, userID, connID)
}

func (r *RouteRepo) List(ctx context.Context, userIDs []int64) (map[int64][]route.Entry, error) {
	return route.List(ctx, r.rdb, userIDs)
}

// Addr 返回本实例的路由地址。ResolveAddr 在开始接受连接之前写入，之后只读。
func (r *RouteRepo) Addr() string {
	return r.addr
}

// MessageClient 是 message 内部接口的客户端，实现 biz.MessageSender 与 biz.CallRecorder。
type MessageClient struct {
	c messagev1.MessageInternalServiceClient
}

// NewMessageClient 创建 message 客户端，默认地址 discovery:///marschat-message。
// 连接是惰性的，message 暂时不可用不影响启动；超时同时也是服务发现首次解析的等待上限。
// SendMessage 按 client_msg_id 幂等、WriteCallRecord 按通话 ID 幂等（重复执行只存一条、返回同一个 seq），
// 连接级失败时重试一次；按接口熔断，重试在外层。
func NewMessageClient(bc *conf.Bootstrap, r *consul.Registry, logger *slog.Logger) (*MessageClient, func(), error) {
	c := bc.GetMessage()
	// 链路追踪与请求指标放在重试、熔断之前，按整次调用统计
	mws, err := telemetry.ClientMiddlewares()
	if err != nil {
		return nil, nil, err
	}
	conn, err := kgrpc.NewClient(context.Background(),
		kgrpc.WithEndpoint(c.GetEndpoint()),
		kgrpc.WithDiscovery(r),
		kgrpc.WithTimeout(c.GetTimeout().AsDuration()),
		kgrpc.WithMiddleware(append(mws,
			retry.Client(messagev1.MessageInternalService_SendMessage_FullMethodName, messagev1.MessageInternalService_WriteCallRecord_FullMethodName),
			circuitbreaker.Client(),
		)...),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("create message client %s: %w", c.GetEndpoint(), err)
	}
	cleanup := func() {
		if err := conn.Close(); err != nil {
			logger.Warn("close client failed", slog.String("upstream", "message"), slog.Any("error", err))
		}
	}
	return &MessageClient{c: messagev1.NewMessageInternalServiceClient(conn)}, cleanup, nil
}

// Send 区分两类错误：带错误原因的错误原样返回，客户端看到具体原因，包括 message 的业务错误，
// 以及熔断打开时的 503 CIRCUITBREAKER（请求没有发出，可以稍后重发）；
// 超时、连接失败等没有错误原因的错误统一为 503 UPSTREAM_UNAVAILABLE（结果未知）。
func (s *MessageClient) Send(ctx context.Context, p auth.Principal, connID int64, m *messagev1.OutgoingMessage) (*messagev1.SendResult, error) {
	resp, err := s.c.SendMessage(ctx, &messagev1.SendMessageRequest{
		SenderId:     p.UserID,
		SessionId:    p.SessionID,
		ConnectionId: connID,
		Message:      m,
	})
	if err != nil {
		return nil, messageError(err)
	}
	return resp.GetResult(), nil
}

// WriteCallRecord 写通话记录，错误规则同 Send。
func (s *MessageClient) WriteCallRecord(ctx context.Context, callID, callerID, calleeID int64, c *messagev1.CallContent) error {
	_, err := s.c.WriteCallRecord(ctx, &messagev1.WriteCallRecordRequest{CallId: callID, CallerId: callerID, CalleeId: calleeID, Call: c})
	if err != nil {
		return messageError(err)
	}
	return nil
}

func messageError(err error) error {
	if e := kerrors.FromError(err); e.Reason != "" {
		return e
	}
	return biz.ErrUpstreamUnavailable(err)
}

type callPermission struct {
	c socialv1.SocialInternalServiceClient
}

// NewCallPermission 创建 social 客户端，默认地址 discovery:///marschat-social。
// 连接是惰性的，social 暂时不可用不影响启动，期间发起呼叫返回 503。
// CheckSendPermission 只读，连接级失败时重试一次；按接口熔断，重试在外层。
func NewCallPermission(bc *conf.Bootstrap, r *consul.Registry, logger *slog.Logger) (biz.CallPermission, func(), error) {
	c := bc.GetSocial()
	// 链路追踪与请求指标放在重试、熔断之前，按整次调用统计
	mws, err := telemetry.ClientMiddlewares()
	if err != nil {
		return nil, nil, err
	}
	conn, err := kgrpc.NewClient(context.Background(),
		kgrpc.WithEndpoint(c.GetEndpoint()),
		kgrpc.WithDiscovery(r),
		kgrpc.WithTimeout(c.GetTimeout().AsDuration()),
		kgrpc.WithMiddleware(append(mws,
			retry.Client(socialv1.SocialInternalService_CheckSendPermission_FullMethodName),
			circuitbreaker.Client(),
		)...),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("create social client %s: %w", c.GetEndpoint(), err)
	}
	cleanup := func() {
		if err := conn.Close(); err != nil {
			logger.Warn("close client failed", slog.String("upstream", "social"), slog.Any("error", err))
		}
	}
	return &callPermission{c: socialv1.NewSocialInternalServiceClient(conn)}, cleanup, nil
}

// CheckCall 沿用单聊发消息的权限规则。social 的业务错误（NOT_FRIENDS、BLOCKED、USER_NOT_FOUND）
// 和熔断错误原样返回，没有错误原因的调用失败统一为 503 UPSTREAM_UNAVAILABLE。
func (p *callPermission) CheckCall(ctx context.Context, callerID, calleeID int64) error {
	_, err := p.c.CheckSendPermission(ctx, &socialv1.CheckSendPermissionRequest{
		SenderId: callerID,
		PeerType: socialv1.PeerType_PEER_TYPE_USER,
		PeerId:   calleeID,
	})
	if err == nil {
		return nil
	}
	if e := kerrors.FromError(err); e.Reason != "" {
		return e
	}
	return biz.ErrUpstreamUnavailable(err)
}

// NewForwarder 创建转发通话信令用的 access 直连客户端（pkg/accessclient），与 delivery 推送用的是同一套实现。
func NewForwarder(bc *conf.Bootstrap, logger *slog.Logger) (biz.Forwarder, func(), error) {
	mws, err := telemetry.ClientMiddlewares()
	if err != nil {
		return nil, nil, err
	}
	c := bc.GetCall()
	cooldown := c.GetForwardCooldown().AsDuration()
	cl := accessclient.New(c.GetForwardTimeout().AsDuration(), func() time.Duration { return cooldown }, logger, mws...)
	return cl, cl.Close, nil
}
