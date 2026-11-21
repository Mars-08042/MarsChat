package data

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/go-kratos/kratos/contrib/registry/consul/v3"
	"github.com/go-kratos/kratos/v3/middleware/circuitbreaker"
	kgrpc "github.com/go-kratos/kratos/v3/transport/grpc"

	accountv1 "github.com/Mars-08042/MarsChat/api/account/v1"
	"github.com/Mars-08042/MarsChat/app/social/internal/biz"
	"github.com/Mars-08042/MarsChat/app/social/internal/conf"
	"github.com/Mars-08042/MarsChat/pkg/middleware/retry"
	"github.com/Mars-08042/MarsChat/pkg/telemetry"
)

// NewAccountClient 创建 account 内部接口的 gRPC 客户端，默认地址 discovery:///marschat-account 经 Consul 发现。
// 连接是惰性的，account 暂时不可用不影响 social 启动。超时由 Kratos 客户端的超时设置控制，
// 同时也是服务发现首次解析的等待上限。BatchGetUsers 只读，连接级失败时重试一次；按接口熔断，重试在外层。
func NewAccountClient(c *conf.Account, r *consul.Registry, logger *slog.Logger) (accountv1.AccountInternalServiceClient, func(), error) {
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
			retry.Client(accountv1.AccountInternalService_BatchGetUsers_FullMethodName),
			circuitbreaker.Client(),
		)...),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("create account client %s: %w", c.GetEndpoint(), err)
	}
	cleanup := func() {
		if err := conn.Close(); err != nil {
			logger.Warn("close account client failed", slog.Any("error", err))
		}
	}
	return accountv1.NewAccountInternalServiceClient(conn), cleanup, nil
}

type userService struct {
	client accountv1.AccountInternalServiceClient
}

// NewUserService 用 account 客户端实现 biz.UserService。
func NewUserService(client accountv1.AccountInternalServiceClient) biz.UserService {
	return &userService{client: client}
}

// BatchGetUsers 一次最多查 100 个（account 接口的上限，与列表每页上限一致）。
// 任何错误都按依赖不可用处理：参数错误属于本服务的编程错误，同样不能当作“用户不存在”。
func (s *userService) BatchGetUsers(ctx context.Context, ids []int64) (map[int64]*biz.UserProfile, error) {
	out := make(map[int64]*biz.UserProfile, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	resp, err := s.client.BatchGetUsers(ctx, &accountv1.BatchGetUsersRequest{UserIds: ids})
	if err != nil {
		return nil, fmt.Errorf("%w: account BatchGetUsers: %w", biz.ErrUnavailable, err)
	}
	for _, u := range resp.GetUsers() {
		out[u.GetId()] = &biz.UserProfile{
			ID: u.GetId(), Nickname: u.GetNickname(), Avatar: u.GetAvatar(), Gender: int32(u.GetGender()),
			Signature: u.GetSignature(), Status: int32(u.GetStatus()),
		}
	}
	return out, nil
}
