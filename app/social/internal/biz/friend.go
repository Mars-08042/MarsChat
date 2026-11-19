package biz

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "github.com/Mars-08042/MarsChat/api/social/v1"
)

// FriendUsecase 负责好友申请、好友关系与拉黑。
type FriendUsecase struct {
	tx       Transaction
	friends  FriendRepo
	requests FriendRequestRepo
	blocks   BlockRepo
	events   EventRepo
	users    UserService
	ids      IDGenerator
	limiter  RateLimiter
	policy   *PolicyHolder
	logger   *slog.Logger
	now      func() time.Time
}

// NewFriendUsecase 创建好友用例。
func NewFriendUsecase(tx Transaction, friends FriendRepo, requests FriendRequestRepo, blocks BlockRepo, events EventRepo,
	users UserService, ids IDGenerator, limiter RateLimiter, policy *PolicyHolder, logger *slog.Logger) *FriendUsecase {
	return &FriendUsecase{
		tx: tx, friends: friends, requests: requests, blocks: blocks, events: events,
		users: users, ids: ids, limiter: limiter, policy: policy, logger: logger, now: nowUTC,
	}
}

// SendResult 是申请好友的结果。
type SendResult struct {
	// Request 是新建或更新的待处理申请；直接成为好友时为 nil
	Request       *FriendRequest
	BecameFriends bool
}

// SendRequest 申请添加好友，规则见设计 3.2。
func (uc *FriendUsecase) SendRequest(ctx context.Context, from, to int64, message string) (*SendResult, error) {
	if from == to {
		return nil, ErrCannotTargetSelf()
	}
	if err := checkRate(ctx, uc.limiter, uc.logger, "friend_request", from, uc.policy.Load().GetFriendRequest()); err != nil {
		return nil, err
	}
	// 调用 account 放在事务外，不在持有数据库连接时等待网络
	if err := uc.ensureUserExists(ctx, to); err != nil {
		return nil, err
	}
	var (
		res *SendResult
		err error
	)
	// 同一对用户并发申请时，后提交的一方撞上唯一索引；重试一次会读到对方刚建的申请，走“更新附言”
	for range 2 {
		res, err = uc.sendOnce(ctx, from, to, message)
		if !errors.Is(err, ErrDuplicate) {
			break
		}
	}
	return res, err
}

func (uc *FriendUsecase) sendOnce(ctx context.Context, from, to int64, message string) (*SendResult, error) {
	now := uc.now()
	var res SendResult
	err := uc.tx.InTx(ctx, func(ctx context.Context) error {
		friends, err := uc.friends.AreFriends(ctx, from, to)
		if err != nil {
			return err
		}
		if friends {
			return ErrAlreadyFriends()
		}
		blocked, err := uc.blocks.EitherBlocked(ctx, from, to)
		if err != nil {
			return err
		}
		if blocked {
			return ErrFriendRequestNotAllowed()
		}

		// 对方已经向我发出待处理申请：直接成为好友
		reverse, err := uc.requests.FindPending(ctx, to, from)
		switch {
		case err == nil:
			ok, err := uc.requests.Handle(ctx, reverse.ID, from, RequestAccepted, now)
			if err != nil {
				return err
			}
			if ok {
				res.BecameFriends = true
				return uc.addFriends(ctx, from, to, now)
			}
			// 对方的申请刚好被处理掉（例如因拉黑失效），按普通申请继续
		case !errors.Is(err, ErrNotFound):
			return err
		}

		own, err := uc.requests.FindPending(ctx, from, to)
		switch {
		case err == nil:
			if err := uc.requests.Refresh(ctx, own.ID, message, now); err != nil {
				return err
			}
			own.Message, own.UpdatedAt = message, now
			res.Request = own
			return nil
		case !errors.Is(err, ErrNotFound):
			return err
		}

		id, err := uc.ids.NextID()
		if err != nil {
			return err
		}
		r := &FriendRequest{
			ID: id, FromUserID: from, ToUserID: to, Message: message, Status: RequestPending,
			CreatedAt: now, UpdatedAt: now,
		}
		if err := uc.requests.Create(ctx, r); err != nil {
			return err
		}
		res.Request = r
		return uc.events.Add(ctx, TopicSocialEvents, idKey(to), &v1.FriendRequestCreated{
			RequestId: id, FromUserId: from, ToUserId: to, Message: message, CreatedAt: timestamppb.New(now),
		}, now)
	})
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// ensureUserExists 经 account 确认对方存在且未封禁。account 不可用时返回 503，不在无法确认时放行。
func (uc *FriendUsecase) ensureUserExists(ctx context.Context, id int64) error {
	users, err := uc.users.BatchGetUsers(ctx, []int64{id})
	if err != nil {
		uc.logger.Warn("check user via account failed", slog.Int64("user_id", id), slog.Any("error", err))
		return ErrDependencyUnavailable()
	}
	u, ok := users[id]
	if !ok || u.Status == userStatusBanned {
		return ErrUserNotFound()
	}
	return nil
}

// addFriends 建立好友关系，并把两人之间剩余的待处理申请一并标记为已同意：
// 两人同时互发申请时会各有一条，只处理其中一条的话，另一方还会看到一条多余的申请。
func (uc *FriendUsecase) addFriends(ctx context.Context, userID, friendID int64, now time.Time) error {
	if err := uc.requests.ResolvePending(ctx, friendID, userID, RequestAccepted, now); err != nil {
		return err
	}
	if err := uc.requests.ResolvePending(ctx, userID, friendID, RequestAccepted, now); err != nil {
		return err
	}
	created, err := uc.friends.AddPair(ctx, userID, friendID, now)
	if err != nil || !created {
		return err
	}
	return uc.events.Add(ctx, TopicSocialEvents, pairKey(userID, friendID), &v1.FriendAdded{
		UserId: userID, FriendId: friendID, CreatedAt: timestamppb.New(now),
	}, now)
}

// AcceptRequest 同意好友申请，只有接收方可以操作。
func (uc *FriendUsecase) AcceptRequest(ctx context.Context, me, id int64) error {
	now := uc.now()
	return uc.tx.InTx(ctx, func(ctx context.Context) error {
		r, err := uc.receivedRequest(ctx, me, id)
		if err != nil {
			return err
		}
		// 条件更新保证并发同意只有一次成功
		ok, err := uc.requests.Handle(ctx, id, me, RequestAccepted, now)
		if err != nil {
			return err
		}
		if !ok {
			return ErrRequestAlreadyHandled()
		}
		return uc.addFriends(ctx, me, r.FromUserID, now)
	})
}

// RejectRequest 拒绝好友申请，只有接收方可以操作。对方之后可以再次申请。
func (uc *FriendUsecase) RejectRequest(ctx context.Context, me, id int64) error {
	if _, err := uc.receivedRequest(ctx, me, id); err != nil {
		return err
	}
	ok, err := uc.requests.Handle(ctx, id, me, RequestRejected, uc.now())
	if err != nil {
		return err
	}
	if !ok {
		return ErrRequestAlreadyHandled()
	}
	return nil
}

// receivedRequest 返回发给 me 的待处理申请。发给别人的申请按不存在处理，不暴露他人的申请是否存在。
func (uc *FriendUsecase) receivedRequest(ctx context.Context, me, id int64) (*FriendRequest, error) {
	r, err := uc.requests.FindByID(ctx, id)
	if errors.Is(err, ErrNotFound) || (err == nil && r.ToUserID != me) {
		return nil, ErrFriendRequestNotFound()
	}
	if err != nil {
		return nil, err
	}
	if r.Status != RequestPending {
		return nil, ErrRequestAlreadyHandled()
	}
	return r, nil
}

// DeleteFriend 删除好友，双方的关系同时删除；对方不是好友时也返回成功。
func (uc *FriendUsecase) DeleteFriend(ctx context.Context, me, other int64) error {
	if me == other {
		return ErrCannotTargetSelf()
	}
	now := uc.now()
	return uc.tx.InTx(ctx, func(ctx context.Context) error {
		deleted, err := uc.friends.DeletePair(ctx, me, other)
		if err != nil || !deleted {
			return err
		}
		return uc.events.Add(ctx, TopicSocialEvents, pairKey(me, other), &v1.FriendDeleted{
			UserId: me, FriendId: other, DeletedAt: timestamppb.New(now),
		}, now)
	})
}

// ListFriends 按好友 ID 递增分页。
func (uc *FriendUsecase) ListFriends(ctx context.Context, me, cursor int64, limit int32) ([]*Friend, int64, error) {
	n := pageLimit(limit)
	list, err := uc.friends.List(ctx, me, cursor, n+1)
	if err != nil {
		return nil, 0, err
	}
	items, next := paginate(list, n, func(f *Friend) int64 { return f.FriendID })
	return items, next, nil
}

// ListReceivedRequests 按时间倒序列出收到的待处理申请。
func (uc *FriendUsecase) ListReceivedRequests(ctx context.Context, me, cursor int64, limit int32) ([]*FriendRequest, int64, error) {
	n := pageLimit(limit)
	list, err := uc.requests.ListReceived(ctx, me, cursor, n+1)
	if err != nil {
		return nil, 0, err
	}
	items, next := paginate(list, n, func(r *FriendRequest) int64 { return r.ID })
	return items, next, nil
}

// ListSentRequests 按时间倒序列出发出的全部申请，申请人能看到对方是否已处理。
func (uc *FriendUsecase) ListSentRequests(ctx context.Context, me, cursor int64, limit int32) ([]*FriendRequest, int64, error) {
	n := pageLimit(limit)
	list, err := uc.requests.ListSent(ctx, me, cursor, n+1)
	if err != nil {
		return nil, 0, err
	}
	items, next := paginate(list, n, func(r *FriendRequest) int64 { return r.ID })
	return items, next, nil
}

// Block 拉黑用户，不删除好友关系；两人之间的待处理申请随之失效。重复拉黑返回成功。
// 拉黑不发事件：被拉黑方不应收到通知。
func (uc *FriendUsecase) Block(ctx context.Context, me, other int64) error {
	if me == other {
		return ErrCannotTargetSelf()
	}
	now := uc.now()
	return uc.tx.InTx(ctx, func(ctx context.Context) error {
		if err := uc.blocks.Add(ctx, me, other, now); err != nil {
			return err
		}
		if err := uc.requests.ResolvePending(ctx, me, other, RequestExpired, now); err != nil {
			return err
		}
		return uc.requests.ResolvePending(ctx, other, me, RequestExpired, now)
	})
}

// Unblock 解除拉黑，没有拉黑时也返回成功。
func (uc *FriendUsecase) Unblock(ctx context.Context, me, other int64) error {
	if me == other {
		return ErrCannotTargetSelf()
	}
	return uc.blocks.Remove(ctx, me, other)
}

// ListBlocked 按被拉黑用户 ID 递增分页。
func (uc *FriendUsecase) ListBlocked(ctx context.Context, me, cursor int64, limit int32) ([]*Block, int64, error) {
	n := pageLimit(limit)
	list, err := uc.blocks.List(ctx, me, cursor, n+1)
	if err != nil {
		return nil, 0, err
	}
	items, next := paginate(list, n, func(b *Block) int64 { return b.BlockedID })
	return items, next, nil
}
