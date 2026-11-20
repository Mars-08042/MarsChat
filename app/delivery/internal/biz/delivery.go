package biz

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	accessv1 "github.com/Mars-08042/MarsChat/api/access/v1"
	accountv1 "github.com/Mars-08042/MarsChat/api/account/v1"
	messagev1 "github.com/Mars-08042/MarsChat/api/message/v1"
	"github.com/Mars-08042/MarsChat/pkg/route"
)

// maxConnsPerCall 与 access.v1.PushRequest、KickRequest 的 connection_ids 上限一致。
const maxConnsPerCall = 1000

// DeliveryUsecase 处理需要推送给在线连接的事件。所有方法都不返回错误，失败只记日志。
type DeliveryUsecase struct {
	routes  RouteRepo
	dedupe  Deduper
	members GroupMembers
	pusher  Pusher
	logger  *slog.Logger
}

// NewDeliveryUsecase 创建投递用例。
func NewDeliveryUsecase(routes RouteRepo, dedupe Deduper, members GroupMembers, pusher Pusher, logger *slog.Logger) *DeliveryUsecase {
	return &DeliveryUsecase{routes: routes, dedupe: dedupe, members: members, pusher: pusher, logger: logger}
}

// OnMessageCreated 把新消息推给会话成员的全部在线连接，排除发起发送的连接（它已经收到 SendAck）。
// 发送者的其他设备、其他标签页也会收到，用于多端同步。
func (uc *DeliveryUsecase) OnMessageCreated(ctx context.Context, eventID string, ev *messagev1.MessageCreated) {
	m := ev.GetMessage()
	if !uc.firstSeen(ctx, eventID) {
		return
	}
	var recipients []int64
	switch m.GetConversationType() {
	case messagev1.ConversationType_CONVERSATION_TYPE_SINGLE:
		recipients = ev.GetMemberIds()
	case messagev1.ConversationType_CONVERSATION_TYPE_GROUP:
		members, err := uc.members.List(ctx, m.GetConversationId())
		if err != nil {
			uc.logger.Warn("list group members failed, push skipped",
				slog.Int64("group_id", m.GetConversationId()), slog.Int64("seq", m.GetSeq()), slog.Any("error", err))
			return
		}
		recipients = memberIDs(members, nil)
	default:
		uc.logger.Error("unknown conversation type, push skipped", slog.String("event_id", eventID), slog.Any("type", m.GetConversationType()))
		return
	}
	frame := &accessv1.ServerFrame{Body: &accessv1.ServerFrame_Message{Message: m}}
	uc.push(ctx, recipients, frame, ev.GetOriginConnectionId())
}

// OnReadPositionUpdated 把已读位置推给本人的全部连接（多端同步未读数），单聊还推给对方（已读回执）。
// 不排除发起端：已读位置只增不减，客户端收到不大于本地的值时忽略。
func (uc *DeliveryUsecase) OnReadPositionUpdated(ctx context.Context, eventID string, ev *messagev1.ReadPositionUpdated) {
	if !uc.firstSeen(ctx, eventID) {
		return
	}
	recipients := []int64{ev.GetPosition().GetUserId()}
	if peer := ev.GetPeerUserId(); peer > 0 {
		recipients = append(recipients, peer)
	}
	frame := &accessv1.ServerFrame{Body: &accessv1.ServerFrame_Read{Read: ev.GetPosition()}}
	uc.push(ctx, recipients, frame, 0)
}

// OnSocialEvent 把 social 事件作为通知推给相关用户（设计 3.7），n.EventId 为事件 ID。
// 推送对象由事件类型决定，见 notificationRecipients；操作者自己的连接也会收到。
func (uc *DeliveryUsecase) OnSocialEvent(ctx context.Context, n *accessv1.Notification) {
	if !uc.firstSeen(ctx, n.GetEventId()) {
		return
	}
	recipients, err := uc.notificationRecipients(ctx, n)
	if err != nil {
		uc.logger.Warn("list group members failed, notification skipped",
			slog.String("event_id", n.GetEventId()), slog.Any("error", err))
		return
	}
	if len(recipients) == 0 {
		return
	}
	uc.push(ctx, recipients, &accessv1.ServerFrame{Body: &accessv1.ServerFrame_Notification{Notification: n}}, 0)
}

// notificationRecipients 返回通知的接收用户，已去重。
// 群事件按群的当前成员推送，再并上事件里的相关用户：新成员、离开的人。这样即使成员列表缓存还没刷新，
// 当事人也能收到；已解散的群查不到成员，用事件里解散前的成员列表。
func (uc *DeliveryUsecase) notificationRecipients(ctx context.Context, n *accessv1.Notification) ([]int64, error) {
	current := func(groupID int64, extra ...int64) ([]int64, error) {
		members, err := uc.members.List(ctx, groupID)
		if err != nil {
			return nil, err
		}
		return memberIDs(members, nil, extra...), nil
	}
	switch b := n.GetBody().(type) {
	case *accessv1.Notification_FriendRequestCreated:
		return uniqueIDs(b.FriendRequestCreated.GetToUserId()), nil
	case *accessv1.Notification_FriendAdded:
		return uniqueIDs(b.FriendAdded.GetUserId(), b.FriendAdded.GetFriendId()), nil
	case *accessv1.Notification_FriendDeleted:
		return uniqueIDs(b.FriendDeleted.GetUserId(), b.FriendDeleted.GetFriendId()), nil
	case *accessv1.Notification_GroupCreated:
		return uniqueIDs(b.GroupCreated.GetMemberIds()...), nil
	case *accessv1.Notification_GroupMembersJoined:
		return current(b.GroupMembersJoined.GetGroupId(), b.GroupMembersJoined.GetUserIds()...)
	case *accessv1.Notification_GroupMemberLeft:
		return current(b.GroupMemberLeft.GetGroupId(), b.GroupMemberLeft.GetUserId())
	case *accessv1.Notification_GroupMemberUpdated:
		return current(b.GroupMemberUpdated.GetGroupId(), b.GroupMemberUpdated.GetUserId())
	case *accessv1.Notification_GroupUpdated:
		return current(b.GroupUpdated.GetGroup().GetId())
	case *accessv1.Notification_GroupDismissed:
		return uniqueIDs(b.GroupDismissed.GetMemberIds()...), nil
	case *accessv1.Notification_GroupJoinRequestCreated:
		members, err := uc.members.List(ctx, b.GroupJoinRequestCreated.GetGroupId())
		if err != nil {
			return nil, err
		}
		return memberIDs(members, func(m GroupMember) bool { return m.Manager }), nil
	}
	return nil, nil
}

// memberIDs 取出满足 keep 的成员 ID（keep 为 nil 时取全部），并上 extra，去重并去掉非正数。
func memberIDs(members []GroupMember, keep func(GroupMember) bool, extra ...int64) []int64 {
	ids := make([]int64, 0, len(members)+len(extra))
	for _, m := range members {
		if keep == nil || keep(m) {
			ids = append(ids, m.UserID)
		}
	}
	return uniqueIDs(append(ids, extra...)...)
}

// uniqueIDs 去重并去掉非正数，保持首次出现的顺序。重复的用户会让同一连接收到两次推送。
func uniqueIDs(ids ...int64) []int64 {
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok || id <= 0 {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// OnSessionRevoked 踢掉该登录会话的全部连接。踢连接本身幂等，不需要去重。
func (uc *DeliveryUsecase) OnSessionRevoked(ctx context.Context, ev *accountv1.SessionRevoked) {
	uc.kick(ctx, ev.GetUserId(), ev.GetReason(), func(e route.Entry) bool { return e.SessionID == ev.GetSessionId() })
}

// OnUserBanned 踢掉该用户的全部连接。
func (uc *DeliveryUsecase) OnUserBanned(ctx context.Context, ev *accountv1.UserBanned) {
	uc.kick(ctx, ev.GetUserId(), accountv1.RevokeReason_REVOKE_REASON_BANNED, func(route.Entry) bool { return true })
}

// firstSeen 先占位再推送：崩溃时宁可漏推（客户端补拉），也不重复推送。
// Redis 不可用时无法去重，路由也查不了，直接跳过。
func (uc *DeliveryUsecase) firstSeen(ctx context.Context, eventID string) bool {
	first, err := uc.dedupe.FirstSeen(ctx, eventID)
	if err != nil {
		uc.logger.Warn("dedupe check failed, push skipped", slog.String("event_id", eventID), slog.Any("error", err))
		return false
	}
	if !first {
		uc.logger.Debug("duplicate event skipped", slog.String("event_id", eventID))
	}
	return first
}

// lookup 查询用户的在线连接，按 access 实例地址分组，keep 返回 false 的连接被排除。
func (uc *DeliveryUsecase) lookup(ctx context.Context, userIDs []int64, keep func(route.Entry) bool) map[string][]int64 {
	routes, err := uc.routes.List(ctx, userIDs)
	if err != nil {
		if routes == nil {
			uc.logger.Warn("list routes failed", slog.Int("users", len(userIDs)), slog.Any("error", err))
			return nil
		}
		uc.logger.Warn("malformed route skipped", slog.Any("error", err))
	}
	byAddr := make(map[string][]int64)
	for _, entries := range routes {
		for _, e := range entries {
			if keep(e) {
				byAddr[e.Addr] = append(byAddr[e.Addr], e.ConnID)
			}
		}
	}
	return byAddr
}

// push 按实例并发推送，一个实例故障不拖慢其他实例。
func (uc *DeliveryUsecase) push(ctx context.Context, userIDs []int64, frame *accessv1.ServerFrame, exclude int64) {
	byAddr := uc.lookup(ctx, userIDs, func(e route.Entry) bool { return e.ConnID != exclude })
	uc.each(byAddr, func(addr string, ids []int64) {
		if _, err := uc.pusher.Push(ctx, addr, ids, frame); err != nil {
			// 路由最多残留一个心跳超时（access 崩溃时），期间推送失败属于预期。
			// 冷却期内的跳过只记 DEBUG：第一次失败已经记过 WARN，实例宕机时每条事件都会走到这里
			if errors.Is(err, ErrInstanceCoolingDown) {
				uc.logger.Debug("push skipped", slog.String("addr", addr), slog.Int("conns", len(ids)))
				return
			}
			uc.logger.Warn("push failed", slog.String("addr", addr), slog.Int("conns", len(ids)), slog.Any("error", err))
		}
	})
}

func (uc *DeliveryUsecase) kick(ctx context.Context, userID int64, reason accountv1.RevokeReason, keep func(route.Entry) bool) {
	byAddr := uc.lookup(ctx, []int64{userID}, keep)
	uc.each(byAddr, func(addr string, ids []int64) {
		n, err := uc.pusher.Kick(ctx, addr, ids, reason)
		if err != nil {
			// 心跳复查吊销名单兜底，最迟一个心跳周期后断开
			uc.logger.Warn("kick failed", slog.String("addr", addr), slog.Int64("user_id", userID), slog.Any("error", err))
			return
		}
		uc.logger.Info("connections kicked", slog.String("addr", addr), slog.Int64("user_id", userID),
			slog.Int("kicked", n), slog.String("reason", reason.String()))
	})
}

// each 对每个实例并发调用 fn，连接数超过单次上限时分批。
func (uc *DeliveryUsecase) each(byAddr map[string][]int64, fn func(addr string, ids []int64)) {
	var wg sync.WaitGroup
	for addr, ids := range byAddr {
		for start := 0; start < len(ids); start += maxConnsPerCall {
			batch := ids[start:min(start+maxConnsPerCall, len(ids))]
			wg.Add(1)
			go func() {
				defer wg.Done()
				fn(addr, batch)
			}()
		}
	}
	wg.Wait()
}
