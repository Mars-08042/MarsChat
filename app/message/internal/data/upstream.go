package data

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/go-kratos/kratos/contrib/registry/consul/v3"
	kerrors "github.com/go-kratos/kratos/v3/errors"
	"github.com/go-kratos/kratos/v3/middleware/circuitbreaker"
	kgrpc "github.com/go-kratos/kratos/v3/transport/grpc"
	"google.golang.org/grpc"

	accountv1 "github.com/Mars-08042/MarsChat/api/account/v1"
	socialv1 "github.com/Mars-08042/MarsChat/api/social/v1"
	"github.com/Mars-08042/MarsChat/app/message/internal/biz"
	"github.com/Mars-08042/MarsChat/app/message/internal/conf"
	"github.com/Mars-08042/MarsChat/pkg/middleware/retry"
)

// dial 创建经 Consul 发现的 gRPC 连接。连接是惰性的，依赖暂时不可用不影响启动；
// 超时同时也是服务发现首次解析的等待上限，并覆盖重试在内的整次调用。
// retryOps 是允许在连接级失败时重试一次的幂等方法；所有方法都按接口熔断，重试在外层。
func dial(name string, c *conf.Upstream, r *consul.Registry, logger *slog.Logger, retryOps ...string) (*grpc.ClientConn, func(), error) {
	conn, err := kgrpc.NewClient(context.Background(),
		kgrpc.WithEndpoint(c.GetEndpoint()),
		kgrpc.WithDiscovery(r),
		kgrpc.WithTimeout(c.GetTimeout().AsDuration()),
		kgrpc.WithMiddleware(retry.Client(retryOps...), circuitbreaker.Client()),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("create %s client %s: %w", name, c.GetEndpoint(), err)
	}
	cleanup := func() {
		if err := conn.Close(); err != nil {
			logger.Warn("close client failed", slog.String("upstream", name), slog.Any("error", err))
		}
	}
	return conn, cleanup, nil
}

// SocialClient 是 social 内部接口的客户端。用具名类型区分两个 Upstream 配置，便于 wire 注入。
type SocialClient struct{ socialv1.SocialInternalServiceClient }

// AccountClient 是 account 内部接口的客户端。
type AccountClient struct{ accountv1.AccountInternalServiceClient }

// NewSocialClient 创建 social 客户端，默认地址 discovery:///marschat-social。
func NewSocialClient(bc *conf.Bootstrap, r *consul.Registry, logger *slog.Logger) (*SocialClient, func(), error) {
	conn, cleanup, err := dial("social", bc.GetSocial(), r, logger,
		socialv1.SocialInternalService_CheckSendPermission_FullMethodName,
		socialv1.SocialInternalService_BatchGetGroups_FullMethodName,
	)
	if err != nil {
		return nil, nil, err
	}
	return &SocialClient{socialv1.NewSocialInternalServiceClient(conn)}, cleanup, nil
}

// NewAccountClient 创建 account 客户端，默认地址 discovery:///marschat-account。
func NewAccountClient(bc *conf.Bootstrap, r *consul.Registry, logger *slog.Logger) (*AccountClient, func(), error) {
	conn, cleanup, err := dial("account", bc.GetAccount(), r, logger,
		accountv1.AccountInternalService_BatchGetUsers_FullMethodName,
	)
	if err != nil {
		return nil, nil, err
	}
	return &AccountClient{accountv1.NewAccountInternalServiceClient(conn)}, cleanup, nil
}

type permissionChecker struct{ c *SocialClient }

// NewPermissionChecker 用 social 的 CheckSendPermission 实现发言权限校验。
func NewPermissionChecker(c *SocialClient) biz.PermissionChecker {
	return &permissionChecker{c: c}
}

// CheckSend 区分两类错误：social 明确拒绝（4xx 业务错误）原样返回，客户端看到具体原因；
// 超时、连接失败、5xx 等按依赖不可用处理，不在无法确认时放行。
func (p *permissionChecker) CheckSend(ctx context.Context, senderID int64, peerType int32, peerID int64) error {
	_, err := p.c.CheckSendPermission(ctx, &socialv1.CheckSendPermissionRequest{
		SenderId: senderID, PeerType: socialv1.PeerType(peerType), PeerId: peerID,
	})
	if err == nil {
		return nil
	}
	if e := kerrors.FromError(err); e.Code >= 400 && e.Code < 500 {
		return e
	}
	return fmt.Errorf("%w: social CheckSendPermission: %w", biz.ErrUnavailable, err)
}

type groupService struct{ c *SocialClient }

// NewGroupService 用 social 的 BatchGetGroups 实现群资料查询。
func NewGroupService(c *SocialClient) biz.GroupService {
	return &groupService{c: c}
}

// BatchGetGroups 一次最多 100 个，与会话列表每页上限一致。
func (s *groupService) BatchGetGroups(ctx context.Context, ids []int64) (map[int64]*biz.GroupBrief, error) {
	out := make(map[int64]*biz.GroupBrief, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	resp, err := s.c.BatchGetGroups(ctx, &socialv1.BatchGetGroupsRequest{GroupIds: ids})
	if err != nil {
		return nil, fmt.Errorf("%w: social BatchGetGroups: %w", biz.ErrUnavailable, err)
	}
	for _, g := range resp.GetGroups() {
		out[g.GetId()] = &biz.GroupBrief{
			ID: g.GetId(), Name: g.GetName(), Avatar: g.GetAvatar(), Status: int32(g.GetStatus()), MemberCount: g.GetMemberCount(),
		}
	}
	return out, nil
}

type userService struct{ c *AccountClient }

// NewUserService 用 account 的 BatchGetUsers 实现资料查询。
func NewUserService(c *AccountClient) biz.UserService {
	return &userService{c: c}
}

func (s *userService) BatchGetUsers(ctx context.Context, ids []int64) (map[int64]*accountv1.UserProfile, error) {
	out := make(map[int64]*accountv1.UserProfile, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	resp, err := s.c.BatchGetUsers(ctx, &accountv1.BatchGetUsersRequest{UserIds: ids})
	if err != nil {
		return nil, fmt.Errorf("%w: account BatchGetUsers: %w", biz.ErrUnavailable, err)
	}
	for _, u := range resp.GetUsers() {
		out[u.GetId()] = u
	}
	return out, nil
}
