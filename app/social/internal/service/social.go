package service

import (
	"context"
	"time"

	v1 "github.com/Mars-08042/MarsChat/api/social/v1"
	"github.com/Mars-08042/MarsChat/app/social/internal/biz"
)

// SocialService 实现对外的 social.v1.SocialService（HTTP 与 gRPC）。
type SocialService struct {
	v1.UnimplementedSocialServiceServer

	friends  *biz.FriendUsecase
	groups   *biz.GroupUsecase
	profiles *biz.ProfileLoader
}

// NewSocialService 创建对外服务。
func NewSocialService(friends *biz.FriendUsecase, groups *biz.GroupUsecase, profiles *biz.ProfileLoader) *SocialService {
	return &SocialService{friends: friends, groups: groups, profiles: profiles}
}

func (s *SocialService) SendFriendRequest(ctx context.Context, req *v1.SendFriendRequestRequest) (*v1.SendFriendRequestResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	res, err := s.friends.SendRequest(ctx, p.UserID, req.GetUserId(), req.GetMessage())
	if err != nil {
		return nil, err
	}
	out := &v1.SendFriendRequestResponse{BecameFriends: res.BecameFriends}
	if res.Request != nil {
		// 刚确认过对方存在，这里不再为资料调用一次 account
		out.Request = toFriendRequest(res.Request, nil)
	}
	return out, nil
}

func (s *SocialService) ListReceivedFriendRequests(ctx context.Context, req *v1.ListReceivedFriendRequestsRequest) (*v1.ListReceivedFriendRequestsResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	list, next, err := s.friends.ListReceivedRequests(ctx, p.UserID, req.GetCursor(), req.GetLimit())
	if err != nil {
		return nil, err
	}
	return &v1.ListReceivedFriendRequestsResponse{Requests: s.friendRequests(ctx, list, p.UserID), NextCursor: next}, nil
}

func (s *SocialService) ListSentFriendRequests(ctx context.Context, req *v1.ListSentFriendRequestsRequest) (*v1.ListSentFriendRequestsResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	list, next, err := s.friends.ListSentRequests(ctx, p.UserID, req.GetCursor(), req.GetLimit())
	if err != nil {
		return nil, err
	}
	return &v1.ListSentFriendRequestsResponse{Requests: s.friendRequests(ctx, list, p.UserID), NextCursor: next}, nil
}

// friendRequests 转换申请列表，peer 为申请中 me 以外的一方。
func (s *SocialService) friendRequests(ctx context.Context, list []*biz.FriendRequest, me int64) []*v1.FriendRequest {
	peerOf := func(r *biz.FriendRequest) int64 {
		if r.FromUserID == me {
			return r.ToUserID
		}
		return r.FromUserID
	}
	ids := make([]int64, len(list))
	for i, r := range list {
		ids[i] = peerOf(r)
	}
	profiles := s.profiles.Load(ctx, ids)
	out := make([]*v1.FriendRequest, len(list))
	for i, r := range list {
		out[i] = toFriendRequest(r, profiles[peerOf(r)])
	}
	return out
}

func (s *SocialService) AcceptFriendRequest(ctx context.Context, req *v1.AcceptFriendRequestRequest) (*v1.AcceptFriendRequestResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.friends.AcceptRequest(ctx, p.UserID, req.GetRequestId()); err != nil {
		return nil, err
	}
	return &v1.AcceptFriendRequestResponse{}, nil
}

func (s *SocialService) RejectFriendRequest(ctx context.Context, req *v1.RejectFriendRequestRequest) (*v1.RejectFriendRequestResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.friends.RejectRequest(ctx, p.UserID, req.GetRequestId()); err != nil {
		return nil, err
	}
	return &v1.RejectFriendRequestResponse{}, nil
}

func (s *SocialService) ListFriends(ctx context.Context, req *v1.ListFriendsRequest) (*v1.ListFriendsResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	list, next, err := s.friends.ListFriends(ctx, p.UserID, req.GetCursor(), req.GetLimit())
	if err != nil {
		return nil, err
	}
	ids := make([]int64, len(list))
	for i, f := range list {
		ids[i] = f.FriendID
	}
	profiles := s.profiles.Load(ctx, ids)
	out := make([]*v1.Friend, len(list))
	for i, f := range list {
		out[i] = &v1.Friend{UserId: f.FriendID, Profile: toProfile(profiles[f.FriendID]), CreatedAt: timestamp(f.CreatedAt)}
	}
	return &v1.ListFriendsResponse{Friends: out, NextCursor: next}, nil
}

func (s *SocialService) DeleteFriend(ctx context.Context, req *v1.DeleteFriendRequest) (*v1.DeleteFriendResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.friends.DeleteFriend(ctx, p.UserID, req.GetUserId()); err != nil {
		return nil, err
	}
	return &v1.DeleteFriendResponse{}, nil
}

func (s *SocialService) BlockUser(ctx context.Context, req *v1.BlockUserRequest) (*v1.BlockUserResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.friends.Block(ctx, p.UserID, req.GetUserId()); err != nil {
		return nil, err
	}
	return &v1.BlockUserResponse{}, nil
}

func (s *SocialService) UnblockUser(ctx context.Context, req *v1.UnblockUserRequest) (*v1.UnblockUserResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.friends.Unblock(ctx, p.UserID, req.GetUserId()); err != nil {
		return nil, err
	}
	return &v1.UnblockUserResponse{}, nil
}

func (s *SocialService) ListBlockedUsers(ctx context.Context, req *v1.ListBlockedUsersRequest) (*v1.ListBlockedUsersResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	list, next, err := s.friends.ListBlocked(ctx, p.UserID, req.GetCursor(), req.GetLimit())
	if err != nil {
		return nil, err
	}
	ids := make([]int64, len(list))
	for i, b := range list {
		ids[i] = b.BlockedID
	}
	profiles := s.profiles.Load(ctx, ids)
	out := make([]*v1.BlockedUser, len(list))
	for i, b := range list {
		out[i] = &v1.BlockedUser{UserId: b.BlockedID, Profile: toProfile(profiles[b.BlockedID]), CreatedAt: timestamp(b.CreatedAt)}
	}
	return &v1.ListBlockedUsersResponse{Users: out, NextCursor: next}, nil
}

func (s *SocialService) CreateGroup(ctx context.Context, req *v1.CreateGroupRequest) (*v1.CreateGroupResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	g, err := s.groups.Create(ctx, p.UserID, biz.CreateGroupInput{
		Name:      req.GetName(),
		Avatar:    req.GetAvatar(),
		Notice:    req.GetNotice(),
		JoinMode:  int32(req.GetJoinMode()),
		MemberIDs: req.GetMemberIds(),
	})
	if err != nil {
		return nil, err
	}
	return &v1.CreateGroupResponse{Group: toGroup(g, biz.RoleOwner)}, nil
}

func (s *SocialService) GetGroup(ctx context.Context, req *v1.GetGroupRequest) (*v1.GetGroupResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	g, role, err := s.groups.Get(ctx, p.UserID, req.GetGroupId())
	if err != nil {
		return nil, err
	}
	return &v1.GetGroupResponse{Group: toGroup(g, role)}, nil
}

func (s *SocialService) UpdateGroup(ctx context.Context, req *v1.UpdateGroupRequest) (*v1.UpdateGroupResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	upd := biz.GroupUpdate{
		Name:    req.Name,
		Avatar:  req.Avatar,
		Notice:  req.Notice,
		MuteAll: req.MuteAll,
	}
	if req.JoinMode != nil {
		m := int32(req.GetJoinMode())
		upd.JoinMode = &m
	}
	g, role, err := s.groups.Update(ctx, p.UserID, req.GetGroupId(), req.GetVersion(), upd)
	if err != nil {
		return nil, err
	}
	return &v1.UpdateGroupResponse{Group: toGroup(g, role)}, nil
}

func (s *SocialService) ListMyGroups(ctx context.Context, req *v1.ListMyGroupsRequest) (*v1.ListMyGroupsResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	list, next, err := s.groups.ListMine(ctx, p.UserID, req.GetCursor(), req.GetLimit())
	if err != nil {
		return nil, err
	}
	out := make([]*v1.Group, len(list))
	for i, mg := range list {
		out[i] = toGroup(mg.Group, mg.Role)
	}
	return &v1.ListMyGroupsResponse{Groups: out, NextCursor: next}, nil
}

func (s *SocialService) JoinGroup(ctx context.Context, req *v1.JoinGroupRequest) (*v1.JoinGroupResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	res, err := s.groups.Join(ctx, p.UserID, req.GetGroupId(), req.GetMessage())
	if err != nil {
		return nil, err
	}
	out := &v1.JoinGroupResponse{Joined: res.Joined}
	if res.Request != nil {
		out.Request = toJoinRequest(res.Request, nil)
	}
	return out, nil
}

func (s *SocialService) ListGroupJoinRequests(ctx context.Context, req *v1.ListGroupJoinRequestsRequest) (*v1.ListGroupJoinRequestsResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	list, next, err := s.groups.ListJoinRequests(ctx, p.UserID, req.GetGroupId(), req.GetCursor(), req.GetLimit())
	if err != nil {
		return nil, err
	}
	ids := make([]int64, len(list))
	for i, r := range list {
		ids[i] = r.UserID
	}
	profiles := s.profiles.Load(ctx, ids)
	out := make([]*v1.GroupJoinRequest, len(list))
	for i, r := range list {
		out[i] = toJoinRequest(r, profiles[r.UserID])
	}
	return &v1.ListGroupJoinRequestsResponse{Requests: out, NextCursor: next}, nil
}

func (s *SocialService) AcceptGroupJoinRequest(ctx context.Context, req *v1.AcceptGroupJoinRequestRequest) (*v1.AcceptGroupJoinRequestResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.groups.HandleJoinRequest(ctx, p.UserID, req.GetRequestId(), true); err != nil {
		return nil, err
	}
	return &v1.AcceptGroupJoinRequestResponse{}, nil
}

func (s *SocialService) RejectGroupJoinRequest(ctx context.Context, req *v1.RejectGroupJoinRequestRequest) (*v1.RejectGroupJoinRequestResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.groups.HandleJoinRequest(ctx, p.UserID, req.GetRequestId(), false); err != nil {
		return nil, err
	}
	return &v1.RejectGroupJoinRequestResponse{}, nil
}

func (s *SocialService) AddGroupMembers(ctx context.Context, req *v1.AddGroupMembersRequest) (*v1.AddGroupMembersResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	added, err := s.groups.AddMembers(ctx, p.UserID, req.GetGroupId(), req.GetUserIds())
	if err != nil {
		return nil, err
	}
	return &v1.AddGroupMembersResponse{AddedUserIds: added}, nil
}

func (s *SocialService) ListGroupMembers(ctx context.Context, req *v1.ListGroupMembersRequest) (*v1.ListGroupMembersResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	list, next, err := s.groups.ListMembers(ctx, p.UserID, req.GetGroupId(), req.GetCursor(), req.GetLimit())
	if err != nil {
		return nil, err
	}
	ids := make([]int64, len(list))
	for i, m := range list {
		ids[i] = m.UserID
	}
	profiles := s.profiles.Load(ctx, ids)
	out := make([]*v1.GroupMember, len(list))
	for i, m := range list {
		out[i] = toMember(m, profiles[m.UserID])
	}
	return &v1.ListGroupMembersResponse{Members: out, NextCursor: next}, nil
}

func (s *SocialService) RemoveGroupMember(ctx context.Context, req *v1.RemoveGroupMemberRequest) (*v1.RemoveGroupMemberResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.groups.RemoveMember(ctx, p.UserID, req.GetGroupId(), req.GetUserId()); err != nil {
		return nil, err
	}
	return &v1.RemoveGroupMemberResponse{}, nil
}

func (s *SocialService) SetGroupMemberRole(ctx context.Context, req *v1.SetGroupMemberRoleRequest) (*v1.SetGroupMemberRoleResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.groups.SetRole(ctx, p.UserID, req.GetGroupId(), req.GetUserId(), int32(req.GetRole())); err != nil {
		return nil, err
	}
	return &v1.SetGroupMemberRoleResponse{}, nil
}

func (s *SocialService) MuteGroupMember(ctx context.Context, req *v1.MuteGroupMemberRequest) (*v1.MuteGroupMemberResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	until, err := s.groups.Mute(ctx, p.UserID, req.GetGroupId(), req.GetUserId(),
		time.Duration(req.GetDurationSeconds())*time.Second)
	if err != nil {
		return nil, err
	}
	return &v1.MuteGroupMemberResponse{MutedUntil: biz.OptionalTimestamp(until)}, nil
}

func (s *SocialService) TransferGroupOwner(ctx context.Context, req *v1.TransferGroupOwnerRequest) (*v1.TransferGroupOwnerResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.groups.Transfer(ctx, p.UserID, req.GetGroupId(), req.GetNewOwnerId()); err != nil {
		return nil, err
	}
	return &v1.TransferGroupOwnerResponse{}, nil
}

func (s *SocialService) LeaveGroup(ctx context.Context, req *v1.LeaveGroupRequest) (*v1.LeaveGroupResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.groups.Leave(ctx, p.UserID, req.GetGroupId()); err != nil {
		return nil, err
	}
	return &v1.LeaveGroupResponse{}, nil
}

func (s *SocialService) DismissGroup(ctx context.Context, req *v1.DismissGroupRequest) (*v1.DismissGroupResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.groups.Dismiss(ctx, p.UserID, p.IsAdmin(), req.GetGroupId()); err != nil {
		return nil, err
	}
	return &v1.DismissGroupResponse{}, nil
}
