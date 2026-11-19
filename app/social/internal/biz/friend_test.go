package biz

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	kerrors "github.com/go-kratos/kratos/v3/errors"

	v1 "github.com/Mars-08042/MarsChat/api/social/v1"
)

// 申请规则表（设计 3.2）中各种拒绝情况。
func TestSendFriendRequestRejections(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(e *env)
		to     int64
		reason string
	}{
		{name: "self", to: 1, reason: "CANNOT_TARGET_SELF"},
		{name: "user not found", to: 404, reason: "USER_NOT_FOUND"},
		{name: "user banned", to: bannedUser, reason: "USER_NOT_FOUND"},
		{name: "already friends", setup: func(e *env) { e.befriend(1, 2) }, to: 2, reason: "ALREADY_FRIENDS"},
		{name: "i blocked target", setup: func(e *env) { _ = e.blocks.Add(context.Background(), 1, 2, e.now) }, to: 2, reason: "FRIEND_REQUEST_NOT_ALLOWED"},
		{name: "target blocked me", setup: func(e *env) { _ = e.blocks.Add(context.Background(), 2, 1, e.now) }, to: 2, reason: "FRIEND_REQUEST_NOT_ALLOWED"},
		{name: "account unavailable", setup: func(e *env) { e.users.err = ErrUnavailable }, to: 2, reason: "DEPENDENCY_UNAVAILABLE"},
		{name: "rate limited", setup: func(e *env) { e.limiter.deny = true }, to: 2, reason: "RATE_LIMITED"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			if tt.setup != nil {
				tt.setup(e)
			}
			_, err := e.friendUC.SendRequest(context.Background(), 1, tt.to, "hi")
			wantReason(t, err, tt.reason)
			if len(e.requests.byID) != 0 || len(e.events.take()) != 0 {
				t.Fatal("rejected request must not be stored or emit events")
			}
		})
	}
}

func TestRateLimitedCarriesRetryAfter(t *testing.T) {
	e := newEnv(t)
	e.limiter.deny = true
	_, err := e.friendUC.SendRequest(context.Background(), 1, 2, "")
	if got := kerrors.FromError(err).GetMetadata()["retry_after_seconds"]; got != "90" {
		t.Fatalf("retry_after_seconds = %q, want 90", got)
	}
}

// 限流器故障时放行，只记日志。
func TestRateLimiterDownAllows(t *testing.T) {
	e := newEnv(t)
	e.limiter.err = errors.New("redis down")
	_, err := e.friendUC.SendRequest(context.Background(), 1, 2, "")
	wantReason(t, err, "")
}

// 第一次申请新建记录并通知接收方；重复申请只更新附言，不新增记录、不重复通知。
func TestSendFriendRequestCreatesThenRefreshes(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	res, err := e.friendUC.SendRequest(ctx, 1, 2, "hi")
	if err != nil || res.BecameFriends || res.Request == nil {
		t.Fatalf("SendRequest = %+v, %v", res, err)
	}
	evs := e.events.take()
	if len(evs) != 1 || evs[0].key != "2" {
		t.Fatalf("events = %v, want one FriendRequestCreated keyed by receiver", eventTypes(evs))
	}
	if ev, ok := evs[0].msg.(*v1.FriendRequestCreated); !ok || ev.GetFromUserId() != 1 || ev.GetToUserId() != 2 {
		t.Fatalf("event = %v", evs[0].msg)
	}

	e.now = e.now.Add(time.Minute)
	res2, err := e.friendUC.SendRequest(ctx, 1, 2, "hello again")
	if err != nil || res2.Request.ID != res.Request.ID {
		t.Fatalf("second SendRequest = %+v, %v; want same request refreshed", res2, err)
	}
	stored, _ := e.requests.FindByID(ctx, res.Request.ID)
	if stored.Message != "hello again" || !stored.UpdatedAt.Equal(e.now) || len(e.requests.byID) != 1 {
		t.Fatalf("stored = %+v, requests = %d", stored, len(e.requests.byID))
	}
	if evs := e.events.take(); len(evs) != 0 {
		t.Fatalf("refresh emitted events %v", eventTypes(evs))
	}
}

// 对方已经向我发出待处理申请时直接成为好友（设计第 8 节第 3 点）。
func TestMutualRequestBecomesFriends(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	first, _ := e.friendUC.SendRequest(ctx, 2, 1, "")
	e.events.take()

	res, err := e.friendUC.SendRequest(ctx, 1, 2, "")
	if err != nil || !res.BecameFriends || res.Request != nil {
		t.Fatalf("SendRequest = %+v, %v; want became friends", res, err)
	}
	if ok, _ := e.friends.AreFriends(ctx, 1, 2); !ok {
		t.Fatal("not friends")
	}
	if ok, _ := e.friends.AreFriends(ctx, 2, 1); !ok {
		t.Fatal("friendship must be stored in both directions")
	}
	if r, _ := e.requests.FindByID(ctx, first.Request.ID); r.Status != RequestAccepted {
		t.Fatalf("reverse request status = %d, want accepted", r.Status)
	}
	evs := e.events.take()
	if len(evs) != 1 || evs[0].key != "1" {
		t.Fatalf("events = %v (keys), want one FriendAdded keyed by smaller id", eventTypes(evs))
	}
	if _, ok := evs[0].msg.(*v1.FriendAdded); !ok {
		t.Fatalf("event = %T", evs[0].msg)
	}
}

// 并发申请撞上唯一索引时重试一次，读到对方刚建的申请后更新附言。
func TestSendFriendRequestRetriesOnDuplicate(t *testing.T) {
	e := newEnv(t)
	concurrent := &FriendRequest{ID: 1, FromUserID: 1, ToUserID: 2, Message: "old", Status: RequestPending, CreatedAt: e.now, UpdatedAt: e.now}
	e.requests.beforeCreate = func() { e.requests.put(concurrent) }

	res, err := e.friendUC.SendRequest(context.Background(), 1, 2, "new")
	if err != nil || res.Request == nil || res.Request.ID != concurrent.ID || res.Request.Message != "new" {
		t.Fatalf("SendRequest = %+v, %v; want concurrent request refreshed", res, err)
	}
	if len(e.requests.byID) != 1 {
		t.Fatalf("requests = %d, want 1", len(e.requests.byID))
	}
}

// 被拒绝后可以再次申请，生成新记录（修正 TD-17）。
func TestRejectedRequestCanBeResent(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	first, _ := e.friendUC.SendRequest(ctx, 1, 2, "")
	wantReason(t, e.friendUC.RejectRequest(ctx, 2, first.Request.ID), "")
	if r, _ := e.requests.FindByID(ctx, first.Request.ID); r.Status != RequestRejected || r.HandledAt == nil {
		t.Fatalf("rejected request = %+v", r)
	}
	second, err := e.friendUC.SendRequest(ctx, 1, 2, "")
	if err != nil || second.Request.ID == first.Request.ID {
		t.Fatalf("resend = %+v, %v; want a new request", second, err)
	}
}

// 只有接收方能处理申请（修正 TD-01），并发同意只成功一次。
func TestAcceptFriendRequest(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	res, _ := e.friendUC.SendRequest(ctx, 1, 2, "")
	id := res.Request.ID
	e.events.take()

	wantReason(t, e.friendUC.AcceptRequest(ctx, 1, id), "FRIEND_REQUEST_NOT_FOUND")
	wantReason(t, e.friendUC.AcceptRequest(ctx, 3, id), "FRIEND_REQUEST_NOT_FOUND")
	wantReason(t, e.friendUC.AcceptRequest(ctx, 2, 404), "FRIEND_REQUEST_NOT_FOUND")
	wantReason(t, e.friendUC.AcceptRequest(ctx, 2, id), "")
	if ok, _ := e.friends.AreFriends(ctx, 2, 1); !ok {
		t.Fatal("not friends after accept")
	}
	if got := eventTypes(e.events.take()); !slices.Equal(got, []string{"FriendAdded"}) {
		t.Fatalf("events = %v", got)
	}
	wantReason(t, e.friendUC.AcceptRequest(ctx, 2, id), "REQUEST_ALREADY_HANDLED")
	wantReason(t, e.friendUC.RejectRequest(ctx, 2, id), "REQUEST_ALREADY_HANDLED")
	// 已是好友后再申请
	_, err := e.friendUC.SendRequest(ctx, 1, 2, "")
	wantReason(t, err, "ALREADY_FRIENDS")
}

// 两人同时互发申请留下两条待处理记录时，同意其中一条会把另一条一并处理。
func TestAcceptResolvesReverseRequest(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.requests.put(&FriendRequest{ID: 1, FromUserID: 1, ToUserID: 2, Status: RequestPending, CreatedAt: e.now, UpdatedAt: e.now})
	e.requests.put(&FriendRequest{ID: 2, FromUserID: 2, ToUserID: 1, Status: RequestPending, CreatedAt: e.now, UpdatedAt: e.now})

	wantReason(t, e.friendUC.AcceptRequest(ctx, 2, 1), "")
	if r, _ := e.requests.FindByID(ctx, 2); r.Status != RequestAccepted {
		t.Fatalf("reverse request status = %d, want accepted", r.Status)
	}
	wantReason(t, e.friendUC.AcceptRequest(ctx, 1, 2), "REQUEST_ALREADY_HANDLED")
}

// 删除好友是幂等的，只在真正删除时发事件。
func TestDeleteFriendIdempotent(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.befriend(1, 2)
	wantReason(t, e.friendUC.DeleteFriend(ctx, 2, 1), "")
	wantReason(t, e.friendUC.DeleteFriend(ctx, 2, 1), "")
	if ok, _ := e.friends.AreFriends(ctx, 1, 2); ok {
		t.Fatal("still friends")
	}
	evs := e.events.take()
	if len(evs) != 1 || evs[0].key != "1" {
		t.Fatalf("events = %v, want one FriendDeleted keyed by smaller id", eventTypes(evs))
	}
	wantReason(t, e.friendUC.DeleteFriend(ctx, 1, 1), "CANNOT_TARGET_SELF")
}

// 拉黑不删除好友关系，两人之间的待处理申请失效，不发事件。
func TestBlockKeepsFriendshipAndExpiresRequests(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.befriend(1, 2)
	e.requests.put(&FriendRequest{ID: 1, FromUserID: 3, ToUserID: 1, Status: RequestPending, CreatedAt: e.now, UpdatedAt: e.now})

	wantReason(t, e.friendUC.Block(ctx, 1, 2), "")
	wantReason(t, e.friendUC.Block(ctx, 1, 2), "")
	wantReason(t, e.friendUC.Block(ctx, 1, 3), "")
	if ok, _ := e.friends.AreFriends(ctx, 1, 2); !ok {
		t.Fatal("block must not delete friendship")
	}
	if r, _ := e.requests.FindByID(ctx, 1); r.Status != RequestExpired {
		t.Fatalf("pending request from blocked user status = %d, want expired", r.Status)
	}
	if evs := e.events.take(); len(evs) != 0 {
		t.Fatalf("block emitted events %v", eventTypes(evs))
	}
	list, next, _ := e.friendUC.ListBlocked(ctx, 1, 0, 0)
	if len(list) != 2 || next != 0 || list[0].BlockedID != 2 {
		t.Fatalf("blocked list = %+v, next %d", list, next)
	}

	wantReason(t, e.friendUC.Unblock(ctx, 1, 2), "")
	wantReason(t, e.friendUC.Unblock(ctx, 1, 2), "")
	if blocked, _ := e.blocks.EitherBlocked(ctx, 1, 2); blocked {
		t.Fatal("still blocked after unblock")
	}
	wantReason(t, e.friendUC.Block(ctx, 1, 1), "CANNOT_TARGET_SELF")
}

// 游标分页：多查一条判断是否还有下一页。
func TestListFriendsPagination(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for _, id := range []int64{2, 3, 4} {
		e.befriend(1, id)
	}
	page1, next, err := e.friendUC.ListFriends(ctx, 1, 0, 2)
	if err != nil || len(page1) != 2 || page1[0].FriendID != 2 || page1[1].FriendID != 3 || next != 3 {
		t.Fatalf("page1 = %+v, next %d, err %v", page1, next, err)
	}
	page2, next, _ := e.friendUC.ListFriends(ctx, 1, next, 2)
	if len(page2) != 1 || page2[0].FriendID != 4 || next != 0 {
		t.Fatalf("page2 = %+v, next %d", page2, next)
	}
}

// 收到的申请只列待处理的，发出的申请列全部状态，都按时间倒序。
func TestListFriendRequests(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	r1, _ := e.friendUC.SendRequest(ctx, 2, 1, "")
	r2, _ := e.friendUC.SendRequest(ctx, 3, 1, "")
	_ = e.friendUC.RejectRequest(ctx, 1, r1.Request.ID)

	received, _, _ := e.friendUC.ListReceivedRequests(ctx, 1, 0, 0)
	if len(received) != 1 || received[0].ID != r2.Request.ID {
		t.Fatalf("received = %+v, want only the pending one", received)
	}
	sent, _, _ := e.friendUC.ListSentRequests(ctx, 2, 0, 0)
	if len(sent) != 1 || sent[0].Status != RequestRejected {
		t.Fatalf("sent = %+v, want the rejected request", sent)
	}
}

// account 不可用时列表资料降级为空，不返回错误。
func TestProfileLoaderDegrades(t *testing.T) {
	e := newEnv(t)
	l := NewProfileLoader(e.users, discard)
	if got := l.Load(context.Background(), []int64{1, 2, 2}); len(got) != 2 {
		t.Fatalf("profiles = %v", keysOf(got))
	}
	e.users.err = ErrUnavailable
	if got := l.Load(context.Background(), []int64{1}); len(got) != 0 {
		t.Fatalf("profiles when account down = %v, want empty", keysOf(got))
	}
}
