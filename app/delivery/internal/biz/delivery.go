package biz

import (
	"context"
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
		ids, err := uc.members.List(ctx, m.GetConversationId())
		if err != nil {
			uc.logger.Warn("list group members failed, push skipped",
				slog.Int64("group_id", m.GetConversationId()), slog.Int64("seq", m.GetSeq()), slog.Any("error", err))
			return
		}
		recipients = ids
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
			// 路由最多残留一个心跳超时（access 崩溃时），期间推送失败属于预期
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
