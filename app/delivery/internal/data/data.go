// Package data 实现 biz 层定义的接口：Redis 在线路由与推送去重、social 群成员查询、access 直连客户端。
package data

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/go-kratos/kratos/contrib/registry/consul/v3"
	kgrpc "github.com/go-kratos/kratos/v3/transport/grpc"
	"github.com/google/wire"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"

	accessv1 "github.com/Mars-08042/MarsChat/api/access/v1"
	accountv1 "github.com/Mars-08042/MarsChat/api/account/v1"
	socialv1 "github.com/Mars-08042/MarsChat/api/social/v1"
	"github.com/Mars-08042/MarsChat/app/delivery/internal/biz"
	"github.com/Mars-08042/MarsChat/app/delivery/internal/conf"
	"github.com/Mars-08042/MarsChat/pkg/bootstrap"
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
func NewGroupMembers(bc *conf.Bootstrap, r *consul.Registry, logger *slog.Logger) (biz.GroupMembers, func(), error) {
	c := bc.GetSocial()
	conn, err := kgrpc.NewClient(context.Background(),
		kgrpc.WithEndpoint(c.GetEndpoint()),
		kgrpc.WithDiscovery(r),
		kgrpc.WithTimeout(c.GetTimeout().AsDuration()),
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
// 每个地址一条 gRPC 连接，建立后复用。连续失败后摘除、监听注册中心变化在重构步骤 5 实现，
// 在那之前，已下线实例的连接留在缓存中，gRPC 会在后台按退避重连。
type pusher struct {
	timeout time.Duration
	logger  *slog.Logger

	mu    sync.Mutex
	conns map[string]*grpc.ClientConn
}

// NewPusher 创建 access 客户端缓存。
func NewPusher(c *conf.Push, logger *slog.Logger) (biz.Pusher, func()) {
	p := &pusher{timeout: c.GetTimeout().AsDuration(), logger: logger, conns: make(map[string]*grpc.ClientConn)}
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

func (p *pusher) client(addr string) (accessv1.AccessInternalServiceClient, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
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

func (p *pusher) Push(ctx context.Context, addr string, connIDs []int64, frame *accessv1.ServerFrame) (int, error) {
	c, err := p.client(addr)
	if err != nil {
		return 0, err
	}
	resp, err := c.Push(ctx, &accessv1.PushRequest{ConnectionIds: connIDs, Frame: frame})
	if err != nil {
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
		return 0, err
	}
	return int(resp.GetKicked()), nil
}
