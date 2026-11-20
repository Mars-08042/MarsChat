// Package data 实现 biz 层定义的接口：Redis 在线路由与推送去重、social 群成员查询、access 直连客户端。
package data

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/go-kratos/kratos/contrib/registry/consul/v3"
	"github.com/go-kratos/kratos/v3/middleware/circuitbreaker"
	kgrpc "github.com/go-kratos/kratos/v3/transport/grpc"
	"github.com/google/wire"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	accessv1 "github.com/Mars-08042/MarsChat/api/access/v1"
	accountv1 "github.com/Mars-08042/MarsChat/api/account/v1"
	socialv1 "github.com/Mars-08042/MarsChat/api/social/v1"
	"github.com/Mars-08042/MarsChat/app/delivery/internal/biz"
	"github.com/Mars-08042/MarsChat/app/delivery/internal/conf"
	"github.com/Mars-08042/MarsChat/pkg/bootstrap"
	"github.com/Mars-08042/MarsChat/pkg/middleware/retry"
	"github.com/Mars-08042/MarsChat/pkg/route"
)

// ProviderSet 是 data 层的依赖提供者。
var ProviderSet = wire.NewSet(
	bootstrap.NewRedis,
	NewRouteRepo,
	NewDeduper,
	NewGroupMembers,
	NewPusher,
)

type routeRepo struct{ rdb *redis.Client }

// NewRouteRepo 创建在线路由的只读仓库。
func NewRouteRepo(rdb *redis.Client) biz.RouteRepo {
	return &routeRepo{rdb: rdb}
}

func (r *routeRepo) List(ctx context.Context, userIDs []int64) (map[int64][]route.Entry, error) {
	return route.List(ctx, r.rdb, userIDs)
}

// dedupeTTL 覆盖消费者重启、再均衡后重复投递的时间窗口。
const dedupeTTL = time.Hour

type deduper struct{ rdb *redis.Client }

// NewDeduper 创建推送去重器，键为 delivery:done:{event_id}。
func NewDeduper(rdb *redis.Client) biz.Deduper {
	return &deduper{rdb: rdb}
}

func (d *deduper) FirstSeen(ctx context.Context, eventID string) (bool, error) {
	return d.rdb.SetNX(ctx, "delivery:done:"+eventID, 1, dedupeTTL).Result()
}

type groupMembers struct{ c socialv1.SocialInternalServiceClient }

// NewGroupMembers 创建 social 客户端，默认地址 discovery:///marschat-social。
// 连接是惰性的，social 暂时不可用不影响启动，期间群消息不推送。
// ListAllGroupMembers 只读，连接级失败时重试一次；按接口熔断，重试在外层。
func NewGroupMembers(bc *conf.Bootstrap, r *consul.Registry, logger *slog.Logger) (biz.GroupMembers, func(), error) {
	c := bc.GetSocial()
	conn, err := kgrpc.NewClient(context.Background(),
		kgrpc.WithEndpoint(c.GetEndpoint()),
		kgrpc.WithDiscovery(r),
		kgrpc.WithTimeout(c.GetTimeout().AsDuration()),
		kgrpc.WithMiddleware(
			retry.Client(socialv1.SocialInternalService_ListAllGroupMembers_FullMethodName),
			circuitbreaker.Client(),
		),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("create social client %s: %w", c.GetEndpoint(), err)
	}
	cleanup := func() {
		if err := conn.Close(); err != nil {
			logger.Warn("close client failed", slog.String("upstream", "social"), slog.Any("error", err))
		}
	}
	return &groupMembers{c: socialv1.NewSocialInternalServiceClient(conn)}, cleanup, nil
}

func (g *groupMembers) List(ctx context.Context, groupID int64) ([]int64, error) {
	resp, err := g.c.ListAllGroupMembers(ctx, &socialv1.ListAllGroupMembersRequest{GroupId: groupID})
	if err != nil {
		return nil, err
	}
	ids := make([]int64, len(resp.GetMembers()))
	for i, m := range resp.GetMembers() {
		ids[i] = m.GetUserId()
	}
	return ids, nil
}

// pusher 按地址直连 access 实例，不经 Consul 负载均衡：推送必须到达持有连接的那个实例（ADR-0005）。
// 每个地址一条 gRPC 连接，建立后复用。不加重试和熔断：换实例没有意义，单个实例的失败由下面的冷却处理。
//
// 调用返回 Unavailable 或 DeadlineExceeded 时，说明实例已经退出或不可达：关闭并移出缓存的连接，
// 地址进入冷却期（policy.push_cooldown），期间的调用直接返回 ErrInstanceCoolingDown。
// 这样 access 实例宕机后，不会每条事件都等一次超时，缓存也不会只增不减（TD-31）；冷却结束后重新拨号。
// 不监听 Consul 中 access 的变化：正在排空的实例已从 Consul 注销，但还在给剩余连接推送。
type pusher struct {
	timeout time.Duration
	policy  *biz.PolicyHolder
	logger  *slog.Logger
	now     func() time.Time

	mu      sync.Mutex
	conns   map[string]*grpc.ClientConn
	cooling map[string]time.Time
}

// NewPusher 创建 access 客户端缓存。
func NewPusher(c *conf.Push, policy *biz.PolicyHolder, logger *slog.Logger) (biz.Pusher, func()) {
	p := newPusher(c.GetTimeout().AsDuration(), policy, logger)
	cleanup := func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		for addr, conn := range p.conns {
			if err := conn.Close(); err != nil {
				logger.Warn("close access client failed", slog.String("addr", addr), slog.Any("error", err))
			}
		}
	}
	return p, cleanup
}

func newPusher(timeout time.Duration, policy *biz.PolicyHolder, logger *slog.Logger) *pusher {
	return &pusher{
		timeout: timeout,
		policy:  policy,
		logger:  logger,
		now:     time.Now,
		conns:   make(map[string]*grpc.ClientConn),
		cooling: make(map[string]time.Time),
	}
}

func (p *pusher) client(addr string) (accessv1.AccessInternalServiceClient, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if until, ok := p.cooling[addr]; ok {
		if p.now().Before(until) {
			return nil, biz.ErrInstanceCoolingDown
		}
		delete(p.cooling, addr)
	}
	conn, ok := p.conns[addr]
	if !ok {
		// NewClient 不建立连接，持锁调用不会阻塞
		var err error
		conn, err = kgrpc.NewClient(context.Background(), kgrpc.WithEndpoint(addr), kgrpc.WithTimeout(p.timeout))
		if err != nil {
			return nil, fmt.Errorf("create access client %s: %w", addr, err)
		}
		p.conns[addr] = conn
	}
	return accessv1.NewAccessInternalServiceClient(conn), nil
}

// unreachable 判断错误是否说明实例不可达。业务错误（如参数校验失败）说明实例是好的，不摘除；
// 调用方自己取消（服务停止）也不算。
func unreachable(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded:
		return true
	}
	return false
}

// evict 在实例不可达时关闭并移出缓存的连接，地址进入冷却期。并发失败的调用只有第一个会关闭连接。
func (p *pusher) evict(addr string, err error) {
	if !unreachable(err) {
		return
	}
	now := p.now()
	cooldown := p.policy.Load().GetPushCooldown().AsDuration()
	p.mu.Lock()
	conn := p.conns[addr]
	delete(p.conns, addr)
	if cooldown > 0 {
		p.cooling[addr] = now.Add(cooldown)
	}
	// 顺手清掉已过期的冷却记录，宕机实例的地址不会再被访问，不清理会一直留着
	for a, until := range p.cooling {
		if !now.Before(until) {
			delete(p.cooling, a)
		}
	}
	p.mu.Unlock()
	if conn == nil {
		return
	}
	if cerr := conn.Close(); cerr != nil {
		p.logger.Debug("close access client failed", slog.String("addr", addr), slog.Any("error", cerr))
	}
	p.logger.Warn("access instance unreachable, connection evicted",
		slog.String("addr", addr), slog.Duration("cooldown", cooldown), slog.Any("error", err))
}

func (p *pusher) Push(ctx context.Context, addr string, connIDs []int64, frame *accessv1.ServerFrame) (int, error) {
	c, err := p.client(addr)
	if err != nil {
		return 0, err
	}
	resp, err := c.Push(ctx, &accessv1.PushRequest{ConnectionIds: connIDs, Frame: frame})
	if err != nil {
		p.evict(addr, err)
		return 0, err
	}
	return int(resp.GetDelivered()), nil
}

func (p *pusher) Kick(ctx context.Context, addr string, connIDs []int64, reason accountv1.RevokeReason) (int, error) {
	c, err := p.client(addr)
	if err != nil {
		return 0, err
	}
	resp, err := c.Kick(ctx, &accessv1.KickRequest{ConnectionIds: connIDs, Reason: reason})
	if err != nil {
		p.evict(addr, err)
		return 0, err
	}
	return int(resp.GetKicked()), nil
}
