package biz

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	v1 "github.com/Mars-08042/MarsChat/api/message/v1"
)

// singleWith 让 alice 与 peer 单聊 n 条消息，返回会话 ID。
func (e *env) singleWith(t *testing.T, peer int64, n int) int64 {
	t.Helper()
	var conv int64
	for i := range n {
		conv = e.send(t, peer, TypeSingle, alice, fmt.Sprintf("%d-%d", peer, i)).ConversationID
	}
	return conv
}

func TestListMessagesPagesBackward(t *testing.T) {
	e := newEnv(t)
	conv := e.singleWith(t, bob, 5)
	ctx := context.Background()

	h, err := e.conv.ListMessages(ctx, alice, conv, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Messages) != 2 || h.Messages[0].Seq != 5 || h.Messages[1].Seq != 4 || h.NextBeforeSeq != 4 {
		t.Fatalf("first page = %d messages, next %d", len(h.Messages), h.NextBeforeSeq)
	}
	h, _ = e.conv.ListMessages(ctx, alice, conv, h.NextBeforeSeq, 3)
	if len(h.Messages) != 3 || h.Messages[2].Seq != 1 || h.NextBeforeSeq != 0 {
		t.Fatalf("last page = %d messages, next %d", len(h.Messages), h.NextBeforeSeq)
	}

	_, err = e.conv.ListMessages(ctx, carol, conv, 0, 10)
	wantReason(t, err, v1.ErrorReason_CONVERSATION_NOT_FOUND.String())
}

func TestGetDifferenceResults(t *testing.T) {
	e := newEnv(t)
	conv := e.singleWith(t, bob, 5)
	ctx := context.Background()

	d, err := e.conv.GetDifference(ctx, alice, conv, 5, 0)
	if err != nil {
		t.Fatal(err)
	}
	if d.Result != v1.DifferenceResult_DIFFERENCE_RESULT_COMPLETE || len(d.Messages) != 0 || d.MaxSeq != 5 {
		t.Fatalf("no gap: %v %d", d.Result, len(d.Messages))
	}

	d, _ = e.conv.GetDifference(ctx, alice, conv, 1, 2)
	if d.Result != v1.DifferenceResult_DIFFERENCE_RESULT_SLICE || len(d.Messages) != 2 || d.Messages[0].Seq != 2 || d.NextFromSeq != 3 {
		t.Fatalf("slice: %v %d next %d", d.Result, len(d.Messages), d.NextFromSeq)
	}
	d, _ = e.conv.GetDifference(ctx, alice, conv, d.NextFromSeq, 2)
	if d.Result != v1.DifferenceResult_DIFFERENCE_RESULT_COMPLETE || len(d.Messages) != 2 || d.Messages[1].Seq != 5 {
		t.Fatalf("exact fit should be complete: %v %d", d.Result, len(d.Messages))
	}

	// limit 超过策略上限时按上限处理
	p := testPolicy()
	p.DifferenceMaxLimit = 3
	e.policy.Store(p)
	d, _ = e.conv.GetDifference(ctx, alice, conv, 0, 200)
	if d.Result != v1.DifferenceResult_DIFFERENCE_RESULT_SLICE || len(d.Messages) != 3 {
		t.Fatalf("capped limit: %v %d", d.Result, len(d.Messages))
	}

	p = testPolicy()
	p.DifferenceTooLong = 3
	e.policy.Store(p)
	d, _ = e.conv.GetDifference(ctx, alice, conv, 1, 0)
	if d.Result != v1.DifferenceResult_DIFFERENCE_RESULT_TOO_LONG || len(d.Messages) != 0 || d.MaxSeq != 5 {
		t.Fatalf("too long: %v %d", d.Result, len(d.Messages))
	}

	_, err = e.conv.GetDifference(ctx, carol, conv, 0, 0)
	wantReason(t, err, v1.ErrorReason_CONVERSATION_NOT_FOUND.String())
}

func TestMarkRead(t *testing.T) {
	e := newEnv(t)
	conv := e.singleWith(t, bob, 3)
	ctx := context.Background()

	got, err := e.conv.MarkRead(ctx, alice, conv, 2)
	if err != nil || got != 2 {
		t.Fatalf("mark read 2: %d %v", got, err)
	}
	ev, ok := e.lastEvent(t).(*v1.ReadPositionUpdated)
	if !ok || ev.GetPosition().GetReadSeq() != 2 || ev.GetPosition().GetUserId() != alice || ev.GetPeerUserId() != bob {
		t.Fatalf("read event = %v", e.lastEvent(t))
	}

	// 不能后退，后退不发事件
	events := e.eventCount()
	if got, _ = e.conv.MarkRead(ctx, alice, conv, 1); got != 2 {
		t.Errorf("read_seq went backwards to %d", got)
	}
	if e.eventCount() != events {
		t.Error("no-op mark read must not emit an event")
	}

	// 超过最新 seq 时按最新 seq 处理
	if got, _ = e.conv.MarkRead(ctx, alice, conv, 100); got != 3 {
		t.Errorf("read_seq = %d, want clamped to 3", got)
	}

	_, err = e.conv.MarkRead(ctx, carol, conv, 1)
	wantReason(t, err, v1.ErrorReason_CONVERSATION_NOT_FOUND.String())
}

func TestListConversations(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	withBob := e.singleWith(t, bob, 3)
	withCarol := e.singleWith(t, carol, 1)
	if _, err := e.conv.MarkRead(ctx, bob, withBob, 2); err != nil {
		t.Fatal(err)
	}
	if err := e.members.OnGroupCreated(ctx, group, []int64{alice, bob}, time.Now()); err != nil {
		t.Fatal(err)
	}

	views, next, err := e.conv.List(ctx, alice, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 || next == 0 {
		t.Fatalf("first page: %d views, next %d", len(views), next)
	}
	rest, next2, _ := e.conv.List(ctx, alice, next, 2)
	if len(rest) != 1 || next2 != 0 {
		t.Fatalf("second page: %d views, next %d", len(rest), next2)
	}
	all := append(views, rest...)

	byID := map[int64]*ConversationView{}
	for _, v := range all {
		byID[v.Conversation.ID] = v
	}
	vb := byID[withBob]
	// bob 发了 3 条，alice 没读；bob 读到 2（他自己的消息让他的位置是 3，标记 2 不后退）
	if vb.Unread() != 3 || vb.LastMessage == nil || vb.LastMessage.Seq != 3 || vb.PeerUser.GetId() != bob || vb.PeerReadSeq != 3 {
		t.Errorf("bob view: unread %d last %v peer %v peer_read %d", vb.Unread(), vb.LastMessage, vb.PeerUser, vb.PeerReadSeq)
	}
	if byID[withCarol].Unread() != 1 {
		t.Errorf("carol unread = %d", byID[withCarol].Unread())
	}
	vg := byID[group]
	if vg.Group == nil || vg.Group.Name != "g" || vg.LastMessage != nil || vg.Unread() != 0 {
		t.Errorf("group view: %+v", vg)
	}

	// 资料服务故障时降级：列表仍然返回，只是没有资料
	e.users.err, e.groups.err = errors.New("account down"), errors.New("social down")
	degraded, _, err := e.conv.List(ctx, alice, 0, 10)
	if err != nil || len(degraded) != 3 {
		t.Fatalf("degraded list: %d %v", len(degraded), err)
	}
	for _, v := range degraded {
		if v.PeerUser != nil || v.Group != nil {
			t.Errorf("expected no profiles when dependencies are down: %+v", v)
		}
	}

	if _, err := e.conv.Get(ctx, alice, withBob); err != nil {
		t.Errorf("get: %v", err)
	}
	_, err = e.conv.Get(ctx, carol, withBob)
	wantReason(t, err, v1.ErrorReason_CONVERSATION_NOT_FOUND.String())
}

func TestMembershipEvents(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := e.members.OnGroupCreated(ctx, group, []int64{alice, bob}, time.Now()); err != nil {
		t.Fatal(err)
	}
	e.send(t, alice, TypeGroup, group, "m1")
	e.send(t, alice, TypeGroup, group, "m2")

	// 新成员能看到历史，但历史不算未读
	if err := e.members.OnMembersJoined(ctx, group, []int64{carol}, time.Now()); err != nil {
		t.Fatal(err)
	}
	v, err := e.conv.Get(ctx, carol, group)
	if err != nil {
		t.Fatal(err)
	}
	if v.Unread() != 0 || v.ReadSeq != 2 {
		t.Errorf("new member: unread %d read %d", v.Unread(), v.ReadSeq)
	}
	if h, err := e.conv.ListMessages(ctx, carol, group, 0, 10); err != nil || len(h.Messages) != 2 {
		t.Errorf("new member history: %v", err)
	}

	// 重放加入事件不改变已读位置
	e.send(t, alice, TypeGroup, group, "m3")
	if err := e.members.OnMembersJoined(ctx, group, []int64{carol}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if v, _ = e.conv.Get(ctx, carol, group); v.Unread() != 1 {
		t.Errorf("replayed join changed read position: unread %d", v.Unread())
	}

	// 退群后读不到，重放退群事件不报错
	for range 2 {
		if err := e.members.OnMemberLeft(ctx, group, carol); err != nil {
			t.Fatal(err)
		}
	}
	_, err = e.conv.ListMessages(ctx, carol, group, 0, 10)
	wantReason(t, err, v1.ErrorReason_CONVERSATION_NOT_FOUND.String())
}
