// Package data 实现 biz 层定义的接口：Redis 在线路由与推送去重、social 群成员查询、access 直连客户端。
package data

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/go-kratos/kratos/contrib/registry/consul/v3"
	"github.com/go-kratos/kratos/v3/middleware/circuitbreaker"
	kgrpc "github.com/go-kratos/kratos/v3/transport/grpc"
	"github.com/google/wire"
	"github.com/redis/go-redis/v9"

	socialv1 "github.com/Mars-08042/MarsChat/api/social/v1"
	"github.com/Mars-08042/MarsChat/app/delivery/internal/biz"
	"github.com/Mars-08042/MarsChat/app/delivery/internal/conf"
	"github.com/Mars-08042/MarsChat/pkg/accessclient"
	"github.com/Mars-08042/MarsChat/pkg/bootstrap"
	"github.com/Mars-08042/MarsChat/pkg/middleware/retry"
	"github.com/Mars-08042/MarsChat/pkg/route"
	"github.com/Mars-08042/MarsChat/pkg/telemetry"
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
			retry.Client(socialv1.SocialInternalService_ListAllGroupMembers_FullMethodName),
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
	return &groupMembers{c: socialv1.NewSocialInternalServiceClient(conn)}, cleanup, nil
}

func (g *groupMembers) List(ctx context.Context, groupID int64) ([]biz.GroupMember, error) {
	resp, err := g.c.ListAllGroupMembers(ctx, &socialv1.ListAllGroupMembersRequest{GroupId: groupID})
	if err != nil {
		return nil, err
	}
	out := make([]biz.GroupMember, len(resp.GetMembers()))
	for i, m := range resp.GetMembers() {
		role := m.GetRole()
		out[i] = biz.GroupMember{
			UserID:  m.GetUserId(),
			Manager: role == socialv1.GroupRole_GROUP_ROLE_OWNER || role == socialv1.GroupRole_GROUP_ROLE_ADMIN,
		}
	}
	return out, nil
}

// NewPusher 创建 access 客户端缓存（pkg/accessclient）：按路由中的地址直连 access 实例，
// 实例不可达时摘除连接并冷却 policy.push_cooldown（TD-31）。
func NewPusher(c *conf.Push, policy *biz.PolicyHolder, logger *slog.Logger) (biz.Pusher, func(), error) {
	mws, err := telemetry.ClientMiddlewares()
	if err != nil {
		return nil, nil, err
	}
	cl := accessclient.New(c.GetTimeout().AsDuration(),
		func() time.Duration { return policy.Load().GetPushCooldown().AsDuration() }, logger, mws...)
	return cl, cl.Close, nil
}
