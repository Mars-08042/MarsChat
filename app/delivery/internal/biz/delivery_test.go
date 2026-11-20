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
	socialv1 "github.com/Mars-08042/MarsChat/api/social/v1"
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
	m     map[int64][]GroupMember
	err   error
	calls int
}

func (f *fakeMembers) List(_ context.Context, groupID int64) ([]GroupMember, error) {
	f.calls++
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
		dedupe: &fakeDedupe{seen: map[string]bool{}},
		// 群 500：用户 1 是群主，2、3 是普通成员（3 不在线）
		members: &fakeMembers{m: map[int64][]GroupMember{500: {{UserID: 1, Manager: true}, {UserID: 2}, {UserID: 3}}}},
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

// notified 返回收到通知的连接，按地址归并，并检查每次推送的帧都是同一条通知。
func notified(t *testing.T, f *fixture, eventID string) map[string][]int64 {
	t.Helper()
	for _, p := range f.pusher.pushes {
		if got := p.frame.GetNotification().GetEventId(); got != eventID {
			t.Fatalf("pushed frame %v, want notification %s", p.frame, eventID)
		}
	}
	return byAddr(f.pusher.pushes)
}

// 各类 social 事件推给设计 3.7 节规定的用户。用户 1 的连接为 a:11、a:12、b:13，用户 2 为 b:21。
func TestSocialNotificationRecipients(t *testing.T) {
	user1 := map[string][]int64{"a": {11, 12}, "b": {13}}
	user2 := map[string][]int64{"b": {21}}
	both := map[string][]int64{"a": {11, 12}, "b": {13, 21}}
	tests := []struct {
		name string
		body *accessv1.Notification
		want map[string][]int64
	}{
		{"好友申请只推给被申请人",
			&accessv1.Notification{Body: &accessv1.Notification_FriendRequestCreated{FriendRequestCreated: &socialv1.FriendRequestCreated{FromUserId: 1, ToUserId: 2}}},
			user2},
		{"成为好友推给双方",
			&accessv1.Notification{Body: &accessv1.Notification_FriendAdded{FriendAdded: &socialv1.FriendAdded{UserId: 1, FriendId: 2}}},
			both},
		{"删除好友推给双方",
			&accessv1.Notification{Body: &accessv1.Notification_FriendDeleted{FriendDeleted: &socialv1.FriendDeleted{UserId: 2, FriendId: 1}}},
			both},
		{"建群推给事件中的成员",
			&accessv1.Notification{Body: &accessv1.Notification_GroupCreated{GroupCreated: &socialv1.GroupCreated{Group: &socialv1.Group{Id: 600}, MemberIds: []int64{2, 2}}}},
			user2},
		{"入群推给当前成员并上新成员（成员缓存可能还没刷新）",
			&accessv1.Notification{Body: &accessv1.Notification_GroupMembersJoined{GroupMembersJoined: &socialv1.GroupMembersJoined{GroupId: 700, UserIds: []int64{2}}}},
			user2},
		{"退群推给当前成员和离开的人",
			&accessv1.Notification{Body: &accessv1.Notification_GroupMemberLeft{GroupMemberLeft: &socialv1.GroupMemberLeft{GroupId: 700, UserId: 1}}},
			user1},
		{"成员变化推给当前成员",
			&accessv1.Notification{Body: &accessv1.Notification_GroupMemberUpdated{GroupMemberUpdated: &socialv1.GroupMemberUpdated{GroupId: 500, UserId: 2}}},
			both},
		{"群资料变化推给当前成员",
			&accessv1.Notification{Body: &accessv1.Notification_GroupUpdated{GroupUpdated: &socialv1.GroupUpdated{Group: &socialv1.Group{Id: 500}}}},
			both},
		{"解散推给事件中解散前的成员",
			&accessv1.Notification{Body: &accessv1.Notification_GroupDismissed{GroupDismissed: &socialv1.GroupDismissed{GroupId: 800, MemberIds: []int64{1, 3}}}},
			user1},
		{"入群申请只推给群主和管理员",
			&accessv1.Notification{Body: &accessv1.Notification_GroupJoinRequestCreated{GroupJoinRequestCreated: &socialv1.GroupJoinRequestCreated{GroupId: 500, UserId: 9}}},
			user1},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture()
			tt.body.EventId = "social:" + string(rune('a'+i))
			f.uc.OnSocialEvent(context.Background(), tt.body)
			got := notified(t, f, tt.body.EventId)
			if len(got) != len(tt.want) {
				t.Fatalf("notified %v, want %v", got, tt.want)
			}
			for addr, ids := range tt.want {
				if !slices.Equal(got[addr], ids) {
					t.Fatalf("notified %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestSocialNotificationDeduplicated(t *testing.T) {
	f := newFixture()
	n := &accessv1.Notification{EventId: "social:1", Body: &accessv1.Notification_FriendAdded{FriendAdded: &socialv1.FriendAdded{UserId: 1, FriendId: 2}}}
	f.uc.OnSocialEvent(context.Background(), n)
	before := len(f.pusher.pushes)
	f.uc.OnSocialEvent(context.Background(), n)
	if len(f.pusher.pushes) != before {
		t.Fatal("duplicate social event notified twice")
	}
}

// 查不到群成员时跳过这条通知，不阻塞后面的事件。
func TestSocialNotificationSkippedWhenMembersUnavailable(t *testing.T) {
	f := newFixture()
	f.members.err = errors.New("social down")
	f.uc.OnSocialEvent(context.Background(), &accessv1.Notification{
		EventId: "social:2",
		Body:    &accessv1.Notification_GroupUpdated{GroupUpdated: &socialv1.GroupUpdated{Group: &socialv1.Group{Id: 500}}},
	})
	if len(f.pusher.pushes) != 0 {
		t.Fatal("notification pushed although members are unknown")
	}
}

// 解散事件自带成员列表，不查询已经解散的群。
func TestGroupDismissedDoesNotQueryMembers(t *testing.T) {
	f := newFixture()
	f.uc.OnSocialEvent(context.Background(), &accessv1.Notification{
		EventId: "social:3",
		Body:    &accessv1.Notification_GroupDismissed{GroupDismissed: &socialv1.GroupDismissed{GroupId: 500, MemberIds: []int64{2}}},
	})
	if f.members.calls != 0 {
		t.Fatalf("members queried %d times for a dismissed group", f.members.calls)
	}
}
