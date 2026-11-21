// Package service 把 proto 接口适配到 biz 用例：参数转换、取当前用户、补全用户资料。
// 参数格式由 protovalidate 中间件在进入这里之前校验。
package service

import (
	"context"
	"time"

	"github.com/google/wire"
	"google.golang.org/protobuf/types/known/timestamppb"

	accountv1 "github.com/Mars-08042/MarsChat/api/account/v1"
	v1 "github.com/Mars-08042/MarsChat/api/social/v1"
	"github.com/Mars-08042/MarsChat/app/social/internal/biz"
	"github.com/Mars-08042/MarsChat/pkg/auth"
)

// ProviderSet 是 service 层的依赖提供者。
var ProviderSet = wire.NewSet(NewSocialService, NewInternalService)

// principal 取当前用户。鉴权中间件保证对外接口一定有值，取不到说明路由配置有误。
func principal(ctx context.Context) (auth.Principal, error) {
	p, ok := auth.FromContext(ctx)
	if !ok {
		return auth.Principal{}, auth.ErrUnauthorized("missing principal")
	}
	return p, nil
}

func toProfile(p *biz.UserProfile) *accountv1.UserProfile {
	if p == nil {
		return nil
	}
	return &accountv1.UserProfile{
		Id:        p.ID,
		Nickname:  p.Nickname,
		Avatar:    p.Avatar,
		Gender:    accountv1.Gender(p.Gender),
		Signature: p.Signature,
		Status:    accountv1.UserStatus(p.Status),
	}
}

func toFriendRequest(r *biz.FriendRequest, peer *biz.UserProfile) *v1.FriendRequest {
	return &v1.FriendRequest{
		Id:         r.ID,
		FromUserId: r.FromUserID,
		ToUserId:   r.ToUserID,
		Message:    r.Message,
		Status:     v1.RequestStatus(r.Status),
		CreatedAt:  timestamppb.New(r.CreatedAt),
		UpdatedAt:  timestamppb.New(r.UpdatedAt),
		Peer:       toProfile(peer),
	}
}

// toGroup 补上本人角色；不是成员时由 biz 层清空公告。
func toGroup(g *biz.Group, role int32) *v1.Group {
	out := biz.GroupProto(g)
	out.MyRole = v1.GroupRole(role)
	return out
}

func toMember(m *biz.Member, profile *biz.UserProfile) *v1.GroupMember {
	return &v1.GroupMember{
		UserId:     m.UserID,
		Role:       v1.GroupRole(m.Role),
		MutedUntil: mutedUntil(m),
		InviterId:  m.InviterID,
		JoinedAt:   timestamppb.New(m.JoinedAt),
		Profile:    toProfile(profile),
	}
}

// mutedUntil 只返回仍然有效的禁言，已过期的按未禁言处理。
func mutedUntil(m *biz.Member) *timestamppb.Timestamp {
	if !m.MutedAt(time.Now()) {
		return nil
	}
	return timestamppb.New(*m.MutedUntil)
}

func timestamp(t time.Time) *timestamppb.Timestamp {
	return timestamppb.New(t)
}

func toJoinRequest(r *biz.JoinRequest, profile *biz.UserProfile) *v1.GroupJoinRequest {
	return &v1.GroupJoinRequest{
		Id:        r.ID,
		GroupId:   r.GroupID,
		UserId:    r.UserID,
		Message:   r.Message,
		Status:    v1.RequestStatus(r.Status),
		HandledBy: r.HandledBy,
		CreatedAt: timestamppb.New(r.CreatedAt),
		UpdatedAt: timestamppb.New(r.UpdatedAt),
		Profile:   toProfile(profile),
	}
}
