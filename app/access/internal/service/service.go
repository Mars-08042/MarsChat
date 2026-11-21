// Package service 实现 access 的内部 gRPC 接口，供 delivery 推送和踢连接。
package service

import (
	"context"

	"github.com/google/wire"

	v1 "github.com/Mars-08042/MarsChat/api/access/v1"
	"github.com/Mars-08042/MarsChat/app/access/internal/biz"
)

// ProviderSet 是 service 层的依赖提供者。
var ProviderSet = wire.NewSet(NewInternalService)

// InternalService 实现 access.v1.AccessInternalService。
type InternalService struct {
	v1.UnimplementedAccessInternalServiceServer
	hub *biz.Hub
}

// NewInternalService 创建内部接口。
func NewInternalService(hub *biz.Hub) *InternalService {
	return &InternalService{hub: hub}
}

// Push 把帧放进本实例上对应连接的发送队列。
func (s *InternalService) Push(_ context.Context, req *v1.PushRequest) (*v1.PushResponse, error) {
	n := s.hub.Push(req.GetConnectionIds(), req.GetFrame())
	return &v1.PushResponse{Delivered: int32(n)}, nil
}

// Kick 断开本实例上对应的连接。
func (s *InternalService) Kick(_ context.Context, req *v1.KickRequest) (*v1.KickResponse, error) {
	n := s.hub.Kick(req.GetConnectionIds(), req.GetReason())
	return &v1.KickResponse{Kicked: int32(n)}, nil
}
