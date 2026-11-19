package biz

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	kerrors "github.com/go-kratos/kratos/v3/errors"

	v1 "github.com/Mars-08042/MarsChat/api/message/v1"
)

const (
	alice int64 = 10
	bob   int64 = 20
	carol int64 = 30
	group int64 = 900
)

func TestSendFirstSingleMessageCreatesConversation(t *testing.T) {
	e := newEnv(t)
	m := e.send(t, bob, TypeSingle, alice, "c1")
	if m.Seq != 1 {
		t.Fatalf("seq = %d, want 1", m.Seq)
	}
	c, err := fakeConvs{e.s}.FindPair(context.Background(), alice, bob)
	if err != nil {
		t.Fatal(err)
	}
	if c.ID != m.ConversationID || c.MaxSeq != 1 || c.Type != TypeSingle {
		t.Fatalf("conversation = %+v", c)
	}
	// 发送者推进到自己的消息，对方从 0 开始，未读 1
	if mb, _ := (fakeMembers{e.s}).Get(context.Background(), c.ID, bob); mb.ReadSeq != 1 {
		t.Errorf("sender read_seq = %d, want 1", mb.ReadSeq)
	}
	if mb, _ := (fakeMembers{e.s}).Get(context.Background(), c.ID, alice); mb.ReadSeq != 0 {
		t.Errorf("receiver read_seq = %d, want 0", mb.ReadSeq)
	}
	ev, ok := e.lastEvent(t).(*v1.MessageCreated)
	if !ok {
		t.Fatalf("last event is %T", e.lastEvent(t))
	}
	if ev.GetOriginConnectionId() != 77 || len(ev.GetMemberIds()) != 2 || ev.GetMessage().GetSeq() != 1 ||
		ev.GetMessage().GetConversationType() != v1.ConversationType_CONVERSATION_TYPE_SINGLE {
		t.Errorf("event = %v", ev)
	}
}

func TestSendSeqIsContiguousAndSharedByBothDirections(t *testing.T) {
	e := newEnv(t)
	var conv int64
	for i := 1; i <= 5; i++ {
		sender, peer := alice, bob
		if i%2 == 0 {
			sender, peer = bob, alice
		}
		m := e.send(t, sender, TypeSingle, peer, fmt.Sprintf("c%d", i))
		if m.Seq != int64(i) {
			t.Fatalf("message %d got seq %d", i, m.Seq)
		}
		if conv == 0 {
			conv = m.ConversationID
		} else if m.ConversationID != conv {
			t.Fatalf("both directions must share one conversation")
		}
	}
}

func TestSendDuplicateReturnsFirstResult(t *testing.T) {
	e := newEnv(t)
	first := e.send(t, alice, TypeSingle, bob, "dup")
	calls, events := e.perm.calls, e.eventCount()

	again := e.send(t, alice, TypeSingle, bob, "dup")
	if again.Seq != first.Seq || again.ConversationID != first.ConversationID {
		t.Fatalf("duplicate got seq %d, want %d", again.Seq, first.Seq)
	}
	if e.perm.calls != calls {
		t.Error("duplicate should be answered before the permission check")
	}
	if e.eventCount() != events {
		t.Error("duplicate must not emit another event")
	}
}

// 模拟重发与首次请求并发：快速路径没查到，校验权限期间首次请求提交了，事务内插入撞上唯一键。
func TestSendConcurrentDuplicateRollsBack(t *testing.T) {
	e := newEnv(t)
	e.send(t, alice, TypeSingle, bob, "warmup")
	c, _ := fakeConvs{e.s}.FindPair(context.Background(), alice, bob)

	e.perm.hook = func() {
		e.perm.hook = nil
		e.s.mu.Lock()
		e.s.msgs[SeqKey{c.ID, 2}] = &Message{ConversationID: c.ID, Seq: 2, SenderID: alice, ClientMsgID: "race"}
		e.s.convs[c.ID].MaxSeq = 2
		e.s.mu.Unlock()
	}
	events := e.eventCount()
	m := e.send(t, alice, TypeSingle, bob, "race")
	if m.Seq != 2 {
		t.Fatalf("got seq %d, want the committed seq 2", m.Seq)
	}
	got, _ := fakeConvs{e.s}.Get(context.Background(), c.ID)
	if got.MaxSeq != 2 {
		t.Errorf("max_seq = %d, want 2 (failed attempt must not allocate a seq)", got.MaxSeq)
	}
	if e.eventCount() != events {
		t.Error("rolled back attempt must not leave an event")
	}
}

func TestSendPermissionErrors(t *testing.T) {
	e := newEnv(t)
	denied := kerrors.New(403, "NOT_FRIENDS", "not friends")
	e.perm.deny = denied
	_, err := e.msg.Send(context.Background(), SendInput{SenderID: alice, ConnectionID: 1, PeerType: TypeSingle, PeerID: bob, ClientMsgID: "x", Content: text("x")})
	wantReason(t, err, "NOT_FRIENDS")

	e.perm.deny = fmt.Errorf("%w: social down", ErrUnavailable)
	_, err = e.msg.Send(context.Background(), SendInput{SenderID: alice, ConnectionID: 1, PeerType: TypeSingle, PeerID: bob, ClientMsgID: "x", Content: text("x")})
	wantReason(t, err, v1.ErrorReason_DEPENDENCY_UNAVAILABLE.String())

	if len(e.s.msgs) != 0 || len(e.s.convs) != 0 {
		t.Error("rejected sends must not write anything")
	}
}

func TestSendRateLimit(t *testing.T) {
	e := newEnv(t)
	e.limiter.deny = true
	_, err := e.msg.Send(context.Background(), SendInput{SenderID: alice, ConnectionID: 1, PeerType: TypeSingle, PeerID: bob, ClientMsgID: "x", Content: text("x")})
	wantReason(t, err, v1.ErrorReason_RATE_LIMITED.String())
	if got := kerrors.FromError(err).GetMetadata()["retry_after_seconds"]; got != "2" {
		t.Errorf("retry_after_seconds = %q, want 2", got)
	}

	// Redis 故障时放行
	e.limiter.deny, e.limiter.err = false, errors.New("redis down")
	e.send(t, alice, TypeSingle, bob, "ok")
}

func TestSendGroupBeforeCreatedEventDoesNotAddMembers(t *testing.T) {
	e := newEnv(t)
	m := e.send(t, alice, TypeGroup, group, "g1")
	if m.ConversationID != group || m.Seq != 1 {
		t.Fatalf("message = %+v", m)
	}
	if len(e.s.members) != 0 {
		t.Error("send must not create group member rows; membership comes from social events")
	}
	ev := e.lastEvent(t).(*v1.MessageCreated)
	if len(ev.GetMemberIds()) != 0 || ev.GetMessage().GetConversationType() != v1.ConversationType_CONVERSATION_TYPE_GROUP {
		t.Errorf("group event = %v", ev)
	}

	// 事件到达后，初始成员的已读位置从 0 开始，事件之前发出的消息也算未读
	if err := e.members.OnGroupCreated(context.Background(), group, []int64{alice, bob}, time.Now()); err != nil {
		t.Fatal(err)
	}
	e.send(t, alice, TypeGroup, group, "g2")
	if mb, _ := (fakeMembers{e.s}).Get(context.Background(), group, alice); mb.ReadSeq != 2 {
		t.Errorf("sender read_seq = %d, want 2", mb.ReadSeq)
	}
	if mb, _ := (fakeMembers{e.s}).Get(context.Background(), group, bob); mb.ReadSeq != 0 {
		t.Errorf("member read_seq = %d, want 0", mb.ReadSeq)
	}
}
