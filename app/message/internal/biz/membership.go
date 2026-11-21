package biz

import (
	"context"
	"time"
)

// MembershipUsecase 根据 social 的群事件维护群会话的成员（设计 3.4）。
//
// 同一个群的事件按群 ID 分区，按顺序到达；这里的写入都是幂等的（插入忽略已存在、删除不存在的行无影响），
// 重复投递只是重放一段前缀，结果不变。成员只随事件变化，发送消息时不补建成员行，
// 避免与并发的退群事件竞争而把刚被移除的人加回来。
type MembershipUsecase struct {
	tx      Transaction
	convs   ConversationRepo
	members MemberRepo
}

// NewMembershipUsecase 创建成员维护用例。
func NewMembershipUsecase(tx Transaction, convs ConversationRepo, members MemberRepo) *MembershipUsecase {
	return &MembershipUsecase{tx: tx, convs: convs, members: members}
}

// OnGroupCreated 建立群会话和全部初始成员。新群的消息对初始成员都算未读，已读位置从 0 开始。
func (uc *MembershipUsecase) OnGroupCreated(ctx context.Context, groupID int64, memberIDs []int64, at time.Time) error {
	at = at.UTC().Truncate(time.Millisecond)
	return uc.tx.InTx(ctx, func(ctx context.Context) error {
		if err := uc.ensureGroup(ctx, groupID, at); err != nil {
			return err
		}
		ids := uniqueIDs(memberIDs)
		ms := make([]*Member, len(ids))
		for i, id := range ids {
			ms[i] = &Member{ConversationID: groupID, UserID: id, JoinedAt: at, UpdatedAt: at}
		}
		return uc.members.AddIgnore(ctx, ms)
	})
}

// OnMembersJoined 加入成员。入群前的历史可以查看，但不计入未读：已读位置取当前的 max_seq。
func (uc *MembershipUsecase) OnMembersJoined(ctx context.Context, groupID int64, userIDs []int64, at time.Time) error {
	at = at.UTC().Truncate(time.Millisecond)
	return uc.tx.InTx(ctx, func(ctx context.Context) error {
		if err := uc.ensureGroup(ctx, groupID, at); err != nil {
			return err
		}
		return uc.members.AddAtLatest(ctx, groupID, uniqueIDs(userIDs), at)
	})
}

// OnMemberLeft 移除成员：会话从该用户的列表中消失，不能再读历史和补拉。
// 群解散时不移除成员，会话保留在列表中，客户端根据群资料显示“已解散”。
func (uc *MembershipUsecase) OnMemberLeft(ctx context.Context, groupID, userID int64) error {
	return uc.members.Remove(ctx, groupID, userID)
}

func (uc *MembershipUsecase) ensureGroup(ctx context.Context, groupID int64, at time.Time) error {
	return uc.convs.CreateIfAbsent(ctx, &Conversation{ID: groupID, Type: TypeGroup, CreatedAt: at, UpdatedAt: at})
}
