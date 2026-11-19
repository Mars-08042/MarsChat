package biz

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"sync"
	"testing"

	accessv1 "github.com/Mars-08042/MarsChat/api/access/v1"
	accountv1 "github.com/Mars-08042/MarsChat/api/account/v1"
	messagev1 "github.com/Mars-08042/MarsChat/api/message/v1"
	"github.com/Mars-08042/MarsChat/pkg/route"
)

type fakeRoutes struct {
	m   map[int64][]route.Entry
	err error
}

func (f *fakeRoutes) List(_ context.Context, userIDs []int64) (map[int64][]route.Entry, error) {
	if f.err != nil && f.m == nil {
		return nil, f.err
	}
	out := map[int64][]route.Entry{}
	for _, id := range userIDs {
		if es, ok := f.m[id]; ok {
			out[id] = es
		}
	}
	return out, f.err
}

type fakeDedupe struct {
	mu   sync.Mutex
	seen map[string]bool
	err  error
}

func (f *fakeDedupe) FirstSeen(_ context.Context, id string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return false, f.err
	}
	if f.seen[id] {
		return false, nil
	}
	f.seen[id] = true
	return true, nil
}

type fakeMembers struct {
	m   map[int64][]int64
	err error
}

func (f *fakeMembers) List(_ context.Context, groupID int64) ([]int64, error) {
	return f.m[groupID], f.err
}

type pushCall struct {
	addr   string
	ids    []int64
	frame  *accessv1.ServerFrame
	reason accountv1.RevokeReason
}

type fakePusher struct {
	mu     sync.Mutex
	pushes []pushCall
	kicks  []pushCall
}

func (f *fakePusher) Push(_ context.Context, addr string, ids []int64, frame *accessv1.ServerFrame) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pushes = append(f.pushes, pushCall{addr: addr, ids: slices.Sorted(slices.Values(ids)), frame: frame})
	return len(ids), nil
}

func (f *fakePusher) Kick(_ context.Context, addr string, ids []int64, reason accountv1.RevokeReason) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kicks = append(f.kicks, pushCall{addr: addr, ids: slices.Sorted(slices.Values(ids)), reason: reason})
	return len(ids), nil
}

// byAddr 把调用按地址归并，便于断言（并发调用的顺序不固定）。
func byAddr(calls []pushCall) map[string][]int64 {
	out := map[string][]int64{}
	for _, c := range calls {
		out[c.addr] = append(out[c.addr], c.ids...)
	}
	for k := range out {
		slices.Sort(out[k])
	}
	return out
}

type fixture struct {
	uc      *DeliveryUsecase
	routes  *fakeRoutes
	dedupe  *fakeDedupe
	members *fakeMembers
	pusher  *fakePusher
}

func newFixture() *fixture {
	f := &fixture{
		// 用户 1：两个连接在 a 实例、一个在 b 实例；用户 2：一个连接在 b 实例
		routes: &fakeRoutes{m: map[int64][]route.Entry{
			1: {{ConnID: 11, SessionID: 101, Addr: "a"}, {ConnID: 12, SessionID: 102, Addr: "a"}, {ConnID: 13, SessionID: 101, Addr: "b"}},
			2: {{ConnID: 21, SessionID: 201, Addr: "b"}},
		}},
		dedupe:  &fakeDedupe{seen: map[string]bool{}},
		members: &fakeMembers{m: map[int64][]int64{500: {1, 2, 3}}},
		pusher:  &fakePusher{},
	}
	f.uc = NewDeliveryUsecase(f.routes, f.dedupe, f.members, f.pusher, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return f
}

func singleMessage(originConn int64) *messagev1.MessageCreated {
	return &messagev1.MessageCreated{
		Message:            &messagev1.Message{ConversationId: 900, ConversationType: messagev1.ConversationType_CONVERSATION_TYPE_SINGLE, Seq: 3, SenderId: 1},
		MemberIds:          []int64{1, 2},
		OriginConnectionId: originConn,
	}
}

func TestSingleMessagePushExcludesOrigin(t *testing.T) {
	f := newFixture()
	f.uc.OnMessageCreated(context.Background(), "message:1", singleMessage(11))
	got := byAddr(f.pusher.pushes)
	if !slices.Equal(got["a"], []int64{12}) || !slices.Equal(got["b"], []int64{13, 21}) || len(got) != 2 {
		t.Fatalf("pushes = %v", got)
	}
	// 每个实例一次调用
	if len(f.pusher.pushes) != 2 {
		t.Fatalf("push calls = %d, want 2", len(f.pusher.pushes))
	}
	if f.pusher.pushes[0].frame.GetMessage().GetSeq() != 3 {
		t.Fatalf("frame = %v", f.pusher.pushes[0].frame)
	}
}

func TestDuplicateEventPushedOnce(t *testing.T) {
	f := newFixture()
	f.uc.OnMessageCreated(context.Background(), "message:1", singleMessage(11))
	f.uc.OnMessageCreated(context.Background(), "message:1", singleMessage(11))
	if len(f.pusher.pushes) != 2 {
		t.Fatalf("push calls = %d, want 2 (one event, two instances)", len(f.pusher.pushes))
	}
}

func TestDedupeFailureSkipsPush(t *testing.T) {
	f := newFixture()
	f.dedupe.err = errors.New("redis down")
	f.uc.OnMessageCreated(context.Background(), "message:1", singleMessage(11))
	if len(f.pusher.pushes) != 0 {
		t.Fatalf("pushed despite dedupe failure: %v", f.pusher.pushes)
	}
}

func TestGroupMessageFansOutToMembers(t *testing.T) {
	f := newFixture()
	ev := &messagev1.MessageCreated{
		Message:            &messagev1.Message{ConversationId: 500, ConversationType: messagev1.ConversationType_CONVERSATION_TYPE_GROUP, Seq: 1},
		OriginConnectionId: 21,
	}
	f.uc.OnMessageCreated(context.Background(), "message:2", ev)
	got := byAddr(f.pusher.pushes)
	if !slices.Equal(got["a"], []int64{11, 12}) || !slices.Equal(got["b"], []int64{13}) || len(got) != 2 {
		t.Fatalf("pushes = %v", got)
	}

	f.members.err = errors.New("social down")
	f.uc.OnMessageCreated(context.Background(), "message:3", ev)
	if len(f.pusher.pushes) != 2 {
		t.Fatalf("pushed despite member lookup failure")
	}
}

func TestReadPositionPushedToSelfAndPeer(t *testing.T) {
	f := newFixture()
	f.uc.OnReadPositionUpdated(context.Background(), "message:4", &messagev1.ReadPositionUpdated{
		Position:   &messagev1.ReadPosition{ConversationId: 900, UserId: 2, ReadSeq: 3},
		PeerUserId: 1,
	})
	got := byAddr(f.pusher.pushes)
	// 不排除任何连接
	if !slices.Equal(got["a"], []int64{11, 12}) || !slices.Equal(got["b"], []int64{13, 21}) {
		t.Fatalf("pushes = %v", got)
	}
	if f.pusher.pushes[0].frame.GetRead().GetReadSeq() != 3 {
		t.Fatalf("frame = %v", f.pusher.pushes[0].frame)
	}
}

func TestSessionRevokedKicksOnlyThatSession(t *testing.T) {
	f := newFixture()
	f.uc.OnSessionRevoked(context.Background(), &accountv1.SessionRevoked{UserId: 1, SessionId: 101, Reason: accountv1.RevokeReason_REVOKE_REASON_KICKED})
	got := byAddr(f.pusher.kicks)
	if !slices.Equal(got["a"], []int64{11}) || !slices.Equal(got["b"], []int64{13}) || len(got) != 2 {
		t.Fatalf("kicks = %v", got)
	}
	for _, k := range f.pusher.kicks {
		if k.reason != accountv1.RevokeReason_REVOKE_REASON_KICKED {
			t.Fatalf("reason = %v", k.reason)
		}
	}
}

func TestUserBannedKicksAllConnections(t *testing.T) {
	f := newFixture()
	f.uc.OnUserBanned(context.Background(), &accountv1.UserBanned{UserId: 1})
	got := byAddr(f.pusher.kicks)
	if !slices.Equal(got["a"], []int64{11, 12}) || !slices.Equal(got["b"], []int64{13}) {
		t.Fatalf("kicks = %v", got)
	}
	if f.pusher.kicks[0].reason != accountv1.RevokeReason_REVOKE_REASON_BANNED {
		t.Fatalf("reason = %v", f.pusher.kicks[0].reason)
	}
}

// 无法解析的路由被跳过，其余照常推送；路由完全查不到时不推送。
func TestRouteErrors(t *testing.T) {
	f := newFixture()
	f.routes.err = errors.New("invalid route value")
	f.uc.OnMessageCreated(context.Background(), "message:5", singleMessage(0))
	if len(byAddr(f.pusher.pushes)) != 2 {
		t.Fatalf("partial routes not pushed: %v", f.pusher.pushes)
	}

	f2 := newFixture()
	f2.routes.m, f2.routes.err = nil, errors.New("redis down")
	f2.uc.OnMessageCreated(context.Background(), "message:6", singleMessage(0))
	if len(f2.pusher.pushes) != 0 {
		t.Fatal("pushed without routes")
	}
}

func TestLargeFanOutIsBatched(t *testing.T) {
	f := newFixture()
	var entries []route.Entry
	for i := range 2500 {
		entries = append(entries, route.Entry{ConnID: int64(10000 + i), Addr: "a"})
	}
	f.routes.m = map[int64][]route.Entry{1: entries}
	f.uc.OnMessageCreated(context.Background(), "message:7", singleMessage(0))
	if len(f.pusher.pushes) != 3 {
		t.Fatalf("push calls = %d, want 3 batches", len(f.pusher.pushes))
	}
	total := 0
	for _, p := range f.pusher.pushes {
		if len(p.ids) > maxConnsPerCall {
			t.Fatalf("batch of %d exceeds limit", len(p.ids))
		}
		total += len(p.ids)
	}
	if total != 2500 {
		t.Fatalf("pushed %d conns, want 2500", total)
	}
}
