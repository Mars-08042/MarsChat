package service

import (
	"context"

	v1 "github.com/Mars-08042/MarsChat/api/account/v1"
	"github.com/Mars-08042/MarsChat/app/account/internal/biz"
)

// InternalService 实现 account.v1.AccountInternalService，只注册在 gRPC 上，供其他服务调用。
type InternalService struct {
	v1.UnimplementedAccountInternalServiceServer

	users *biz.UserUsecase
}

// NewInternalService 创建内部服务。
func NewInternalService(users *biz.UserUsecase) *InternalService {
	return &InternalService{users: users}
}

func (s *InternalService) BatchGetUsers(ctx context.Context, req *v1.BatchGetUsersRequest) (*v1.BatchGetUsersResponse, error) {
	list, err := s.users.BatchGet(ctx, req.GetUserIds())
	if err != nil {
		return nil, err
	}
	out := make([]*v1.UserProfile, len(list))
	for i, u := range list {
		out[i] = toProfile(u)
	}
	return &v1.BatchGetUsersResponse{Users: out}, nil
}
