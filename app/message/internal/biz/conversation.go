package biz

import (
	"context"
	"errors"
	"log/slog"
	"math"

	accountv1 "github.com/Mars-08042/MarsChat/api/account/v1"
	v1 "github.com/Mars-08042/MarsChat/api/message/v1"
)

// ConversationUsecase 处理会话列表、历史、补拉与已读。
type ConversationUsecase struct {
	tx      Transaction
	convs   ConversationRepo
	members MemberRepo
	msgs    MessageRepo
	events  EventRepo
	users   UserService
	groups  GroupService
	policy  *PolicyHolder
	logger  *slog.Logger
}

// NewConversationUsecase 创建会话用例。
func NewConversationUsecase(tx Transaction, convs ConversationRepo, members MemberRepo, msgs MessageRepo, events EventRepo,
	users UserService, groups GroupService, policy *PolicyHolder, logger *slog.Logger) *ConversationUsecase {
	return &ConversationUsecase{tx: tx, convs: convs, members: members, msgs: msgs, events: events,
		users: users, groups: groups, policy: policy, logger: logger}
}

// ConversationView 是会话列表中的一项。
type ConversationView struct {
	Conversation *Conversation
	ReadSeq      int64
	// 单聊对方的已读位置
	PeerReadSeq int64
	// 单聊对方的资料，account 不可用时为 nil
	PeerUser *accountv1.UserProfile
	// 群资料，social 不可用时为 nil
	Group       *GroupBrief
	LastMessage *Message
}

// Unread 是未读数。
func (v *ConversationView) Unread() int64 {
	return max(0, v.Conversation.MaxSeq-v.ReadSeq)
}

// List 按会话 ID 升序分页列出我的会话。
func (uc *ConversationUsecase) List(ctx context.Context, me, cursor int64, limit int32) ([]*ConversationView, int64, error) {
	n := pageLimit(limit, defaultPageSize, maxPageSize)
	members, err := uc.members.ListByUser(ctx, me, cursor, n+1)
	if err != nil {
		return nil, 0, err
	}
	var next int64
	if len(members) > n {
		members = members[:n]
		next = members[n-1].ConversationID
	}
	views, err := uc.views(ctx, me, members)
	if err != nil {
		return nil, 0, err
	}
	return views, next, nil
}

// Get 返回单个会话。
func (uc *ConversationUsecase) Get(ctx context.Context, me, id int64) (*ConversationView, error) {
	m, err := uc.member(ctx, id, me)
	if err != nil {
		return nil, err
	}
	views, err := uc.views(ctx, me, []*Member{m})
	if err != nil {
		return nil, err
	}
	if len(views) == 0 {
		return nil, ErrConversationNotFound()
	}
	return views[0], nil
}

// views 为成员行补全会话、最后一条消息、单聊对方的已读位置和资料。
// 资料获取失败时降级为空，列表仍然可用（与 social 的列表一致）。
func (uc *ConversationUsecase) views(ctx context.Context, me int64, members []*Member) ([]*ConversationView, error) {
	if len(members) == 0 {
		return nil, nil
	}
	ids := make([]int64, len(members))
	for i, m := range members {
		ids[i] = m.ConversationID
	}
	convs, err := uc.convs.BatchGet(ctx, ids)
	if err != nil {
		return nil, err
	}

	var (
		keys     []SeqKey
		singles  []int64
		peerIDs  []int64
		groupIDs []int64
	)
	for _, id := range ids {
		c := convs[id]
		if c == nil {
			continue
		}
		if c.MaxSeq > 0 {
			keys = append(keys, SeqKey{ConversationID: c.ID, Seq: c.MaxSeq})
		}
		if c.Type == TypeSingle {
			singles = append(singles, c.ID)
			peerIDs = append(peerIDs, c.Peer(me))
		} else {
			groupIDs = append(groupIDs, c.ID)
		}
	}
	last, err := uc.msgs.BatchGet(ctx, keys)
	if err != nil {
		return nil, err
	}
	others, err := uc.members.ListOthers(ctx, singles, me)
	if err != nil {
		return nil, err
	}
	users := uc.loadUsers(ctx, peerIDs)
	groups := uc.loadGroups(ctx, groupIDs)

	views := make([]*ConversationView, 0, len(members))
	for _, m := range members {
		c := convs[m.ConversationID]
		if c == nil {
			// 成员行存在而会话行不存在，说明数据不一致，跳过并记录
			uc.logger.Error("conversation row missing for member", slog.Int64("conversation_id", m.ConversationID))
			continue
		}
		v := &ConversationView{
			Conversation: c,
			ReadSeq:      m.ReadSeq,
			LastMessage:  last[SeqKey{ConversationID: c.ID, Seq: c.MaxSeq}],
		}
		if c.Type == TypeSingle {
			if o := others[c.ID]; o != nil {
				v.PeerReadSeq = o.ReadSeq
			}
			v.PeerUser = users[c.Peer(me)]
		} else {
			v.Group = groups[c.ID]
		}
		views = append(views, v)
	}
	return views, nil
}

func (uc *ConversationUsecase) loadUsers(ctx context.Context, ids []int64) map[int64]*accountv1.UserProfile {
	if len(ids) == 0 {
		return nil
	}
	m, err := uc.users.BatchGetUsers(ctx, uniqueIDs(ids))
	if err != nil {
		uc.logger.Warn("load user profiles failed, returning conversations without profiles", slog.Any("error", err))
		return nil
	}
	return m
}

func (uc *ConversationUsecase) loadGroups(ctx context.Context, ids []int64) map[int64]*GroupBrief {
	if len(ids) == 0 {
		return nil
	}
	m, err := uc.groups.BatchGetGroups(ctx, ids)
	if err != nil {
		uc.logger.Warn("load groups failed, returning conversations without group profiles", slog.Any("error", err))
		return nil
	}
	return m
}

// member 返回会话成员行，不是成员时返回 CONVERSATION_NOT_FOUND。
func (uc *ConversationUsecase) member(ctx context.Context, convID, me int64) (*Member, error) {
	m, err := uc.members.Get(ctx, convID, me)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrConversationNotFound()
	}
	return m, err
}

// access 校验成员身份并返回会话。
func (uc *ConversationUsecase) access(ctx context.Context, convID, me int64) (*Conversation, *Member, error) {
	m, err := uc.member(ctx, convID, me)
	if err != nil {
		return nil, nil, err
	}
	c, err := uc.convs.Get(ctx, convID)
	if errors.Is(err, ErrNotFound) {
		return nil, nil, ErrConversationNotFound()
	}
	if err != nil {
		return nil, nil, err
	}
	return c, m, nil
}

// History 是一页历史消息。
type History struct {
	Conversation *Conversation
	Messages     []*Message
	// NextBeforeSeq 为 0 表示没有更早的消息
	NextBeforeSeq int64
}

// ListMessages 从 beforeSeq 往前翻页，beforeSeq 为 0 表示从最新开始。
func (uc *ConversationUsecase) ListMessages(ctx context.Context, me, convID, beforeSeq int64, limit int32) (*History, error) {
	c, _, err := uc.access(ctx, convID, me)
	if err != nil {
		return nil, err
	}
	if beforeSeq == 0 {
		beforeSeq = math.MaxInt64
	}
	n := pageLimit(limit, defaultPageSize, maxPageSize)
	msgs, err := uc.msgs.ListBefore(ctx, convID, beforeSeq, n+1)
	if err != nil {
		return nil, err
	}
	h := &History{Conversation: c, Messages: msgs}
	if len(msgs) > n {
		h.Messages = msgs[:n]
		h.NextBeforeSeq = msgs[n-1].Seq
	}
	return h, nil
}

// Difference 是补拉结果。
type Difference struct {
	Conversation *Conversation
	Result       v1.DifferenceResult
	Messages     []*Message
	NextFromSeq  int64
	MaxSeq       int64
}

// GetDifference 返回 fromSeq 之后的消息（设计 3.4 的三种结果）。
func (uc *ConversationUsecase) GetDifference(ctx context.Context, me, convID, fromSeq int64, limit int32) (*Difference, error) {
	c, _, err := uc.access(ctx, convID, me)
	if err != nil {
		return nil, err
	}
	p := uc.policy.Load()
	d := &Difference{Conversation: c, MaxSeq: c.MaxSeq, Result: v1.DifferenceResult_DIFFERENCE_RESULT_COMPLETE}
	if fromSeq >= c.MaxSeq {
		return d, nil
	}
	if c.MaxSeq-fromSeq > p.GetDifferenceTooLong() {
		d.Result = v1.DifferenceResult_DIFFERENCE_RESULT_TOO_LONG
		return d, nil
	}
	n := pageLimit(limit, defaultDifferenceSize, int(p.GetDifferenceMaxLimit()))
	msgs, err := uc.msgs.ListAfter(ctx, convID, fromSeq, n)
	if err != nil {
		return nil, err
	}
	d.Messages = msgs
	if len(msgs) == 0 {
		return d, nil
	}
	lastSeq := msgs[len(msgs)-1].Seq
	// 读会话行之后又有新消息提交时，返回的消息可能超过读到的 max_seq
	d.MaxSeq = max(d.MaxSeq, lastSeq)
	if len(msgs) == n && lastSeq < c.MaxSeq {
		d.Result = v1.DifferenceResult_DIFFERENCE_RESULT_SLICE
		d.NextFromSeq = lastSeq
	}
	return d, nil
}

// MarkRead 把已读位置推进到 seq（不超过会话最新 seq），返回生效后的位置。
// 位置变化时在同一事务中写 ReadPositionUpdated 事件，推送给本人的其他设备和单聊对方。
func (uc *ConversationUsecase) MarkRead(ctx context.Context, me, convID, seq int64) (int64, error) {
	c, m, err := uc.access(ctx, convID, me)
	if err != nil {
		return 0, err
	}
	target := min(seq, c.MaxSeq)
	if target <= m.ReadSeq {
		return m.ReadSeq, nil
	}
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		now := nowUTC()
		changed, err := uc.members.AdvanceRead(ctx, convID, me, target, now)
		if err != nil || !changed {
			return err
		}
		ev := &v1.ReadPositionUpdated{
			Position:         &v1.ReadPosition{ConversationId: convID, UserId: me, ReadSeq: target},
			ConversationType: v1.ConversationType(c.Type),
		}
		if c.Type == TypeSingle {
			ev.PeerUserId = c.Peer(me)
		}
		return uc.events.Add(ctx, TopicMessageEvents, idKey(convID), ev, now)
	})
	if err != nil {
		return 0, err
	}
	// 并发的另一次标记可能推进得更远，这里返回本次请求生效的下限，客户端以推送为准
	return target, nil
}
