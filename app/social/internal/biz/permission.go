package biz

import (
	"context"
	"time"
)

// 取值与 social.v1.PeerType 一致。
const (
	PeerUser  int32 = 1
	PeerGroup int32 = 2
)

// PermissionUsecase 校验发言权限，message 每条消息调用一次。
type PermissionUsecase struct {
	friends FriendRepo
	blocks  BlockRepo
	groups  *GroupUsecase
	members MemberRepo
	now     func() time.Time
}

// NewPermissionUsecase 创建发言权限用例。
func NewPermissionUsecase(friends FriendRepo, blocks BlockRepo, groups *GroupUsecase, members MemberRepo) *PermissionUsecase {
	return &PermissionUsecase{friends: friends, blocks: blocks, groups: groups, members: members, now: time.Now}
}

// CheckSend 允许时返回 nil，不允许时返回带原因的业务错误。
//   - 单聊：双方都没有拉黑对方，且是好友。拉黑优先于好友检查，被拉黑时返回 BLOCKED。
//   - 群聊：群正常、发送者是成员、未被单人禁言；全员禁言时只有群主和管理员能发言。
func (uc *PermissionUsecase) CheckSend(ctx context.Context, senderID int64, peerType int32, peerID int64) error {
	switch peerType {
	case PeerUser:
		return uc.checkUser(ctx, senderID, peerID)
	case PeerGroup:
		return uc.checkGroup(ctx, senderID, peerID)
	default:
		return ErrInvalidArgument("peer_type: unsupported")
	}
}

func (uc *PermissionUsecase) checkUser(ctx context.Context, senderID, peerID int64) error {
	if senderID == peerID {
		return ErrCannotTargetSelf()
	}
	blocked, err := uc.blocks.EitherBlocked(ctx, senderID, peerID)
	if err != nil {
		return err
	}
	if blocked {
		return ErrBlocked()
	}
	friends, err := uc.friends.AreFriends(ctx, senderID, peerID)
	if err != nil {
		return err
	}
	if !friends {
		return ErrNotFriends()
	}
	return nil
}

func (uc *PermissionUsecase) checkGroup(ctx context.Context, senderID, groupID int64) error {
	g, err := uc.groups.cachedGroup(ctx, groupID)
	if err != nil {
		return err
	}
	if g.Status == GroupDismissed {
		return ErrGroupDismissed()
	}
	members, err := uc.members.GetAllCached(ctx, groupID)
	if err != nil {
		return err
	}
	for _, m := range members {
		if m.UserID != senderID {
			continue
		}
		if m.MutedAt(uc.now()) {
			return ErrMemberMuted()
		}
		if g.MuteAll && m.Role == RoleMember {
			return ErrGroupMuted()
		}
		return nil
	}
	return ErrNotGroupMember()
}
