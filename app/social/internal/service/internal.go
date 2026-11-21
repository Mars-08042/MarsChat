package service

import (
	"context"

	v1 "github.com/Mars-08042/MarsChat/api/social/v1"
	"github.com/Mars-08042/MarsChat/app/social/internal/biz"
)

// InternalService 实现 social.v1.SocialInternalService，只注册在 gRPC 上，供 message、delivery 调用。
type InternalService struct {
	v1.UnimplementedSocialInternalServiceServer

	permissions *biz.PermissionUsecase
	groups      *biz.GroupUsecase
}

// NewInternalService 创建内部服务。
func NewInternalService(permissions *biz.PermissionUsecase, groups *biz.GroupUsecase) *InternalService {
	return &InternalService{permissions: permissions, groups: groups}
}

func (s *InternalService) CheckSendPermission(ctx context.Context, req *v1.CheckSendPermissionRequest) (*v1.CheckSendPermissionResponse, error) {
	if err := s.permissions.CheckSend(ctx, req.GetSenderId(), int32(req.GetPeerType()), req.GetPeerId()); err != nil {
		return nil, err
	}
	return &v1.CheckSendPermissionResponse{}, nil
}

func (s *InternalService) ListAllGroupMembers(ctx context.Context, req *v1.ListAllGroupMembersRequest) (*v1.ListAllGroupMembersResponse, error) {
	list, err := s.groups.ListAllMembers(ctx, req.GetGroupId())
	if err != nil {
		return nil, err
	}
	out := make([]*v1.GroupMemberState, len(list))
	for i, m := range list {
		out[i] = &v1.GroupMemberState{UserId: m.UserID, Role: v1.GroupRole(m.Role), MutedUntil: biz.OptionalTimestamp(m.MutedUntil)}
	}
	return &v1.ListAllGroupMembersResponse{Members: out}, nil
}

func (s *InternalService) BatchGetGroups(ctx context.Context, req *v1.BatchGetGroupsRequest) (*v1.BatchGetGroupsResponse, error) {
	list, err := s.groups.BatchGet(ctx, req.GetGroupIds())
	if err != nil {
		return nil, err
	}
	out := make([]*v1.Group, len(list))
	for i, g := range list {
		out[i] = biz.GroupProto(g)
	}
	return &v1.BatchGetGroupsResponse{Groups: out}, nil
}
