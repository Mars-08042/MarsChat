package biz

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/protobuf/proto"

	v1 "github.com/Mars-08042/MarsChat/api/message/v1"
)

func completedCall(seconds int32) *v1.CallContent {
	return &v1.CallContent{Media: v1.CallRecordMedia_CALL_RECORD_MEDIA_VIDEO, Result: v1.CallResult_CALL_RESULT_COMPLETED, DurationSeconds: seconds}
}

func (e *env) writeCallRecord(t *testing.T, callID, caller, callee int64, c *v1.CallContent) *Message {
	t.Helper()
	m, err := e.msg.WriteCallRecord(context.Background(), CallRecordInput{CallID: callID, CallerID: caller, CalleeID: callee, Call: c})
	if err != nil {
		t.Fatalf("write call record %d: %v", callID, err)
	}
	return m
}

// 通话记录接在已有的单聊会话后面，发送者是主叫；不限流、不校验发言权限，推送不排除任何连接。
func TestWriteCallRecord(t *testing.T) {
	e := newEnv(t)
	first := e.send(t, bob, TypeSingle, alice, "hi")
	e.limiter.deny = true
	e.perm.deny = errors.New("not friends")
	calls := e.perm.calls

	m := e.writeCallRecord(t, 501, alice, bob, completedCall(83))
	if m.ConversationID != first.ConversationID || m.Seq != 2 || m.SenderID != alice || m.ClientMsgID != "call:501" {
		t.Fatalf("record = %+v", m)
	}
	if !proto.Equal(m.Content.GetCall(), completedCall(83)) {
		t.Fatalf("content = %v", m.Content)
	}
	if e.perm.calls != calls {
		t.Error("call records must skip the send permission check")
	}
	ev := e.lastEvent(t).(*v1.MessageCreated)
	if ev.GetOriginConnectionId() != 0 || len(ev.GetMemberIds()) != 2 || ev.GetMessage().GetSenderId() != alice {
		t.Errorf("event = %v", ev)
	}
	// 主叫的已读位置推进到记录，被叫多一条未读
	if mb, _ := (fakeMembers{e.s}).Get(context.Background(), m.ConversationID, alice); mb.ReadSeq != 2 {
		t.Errorf("caller read_seq = %d, want 2", mb.ReadSeq)
	}
	if mb, _ := (fakeMembers{e.s}).Get(context.Background(), m.ConversationID, bob); mb.ReadSeq != 1 {
		t.Errorf("callee read_seq = %d, want 1", mb.ReadSeq)
	}
}

// 两人之间第一条消息就是通话记录时，会话随记录一起建立。
func TestWriteCallRecordCreatesConversation(t *testing.T) {
	e := newEnv(t)
	m := e.writeCallRecord(t, 502, bob, alice, &v1.CallContent{Media: v1.CallRecordMedia_CALL_RECORD_MEDIA_AUDIO, Result: v1.CallResult_CALL_RESULT_MISSED})
	c, err := fakeConvs{e.s}.FindPair(context.Background(), alice, bob)
	if err != nil {
		t.Fatal(err)
	}
	if c.ID != m.ConversationID || m.Seq != 1 || m.SenderID != bob {
		t.Fatalf("record = %+v, conversation = %+v", m, c)
	}
}

// access 重试时同一通通话只存一条；客户端用相似的 client_msg_id 发的消息不会占掉记录的幂等键。
func TestWriteCallRecordIdempotent(t *testing.T) {
	e := newEnv(t)
	e.send(t, alice, TypeSingle, bob, "call-503")
	first := e.writeCallRecord(t, 503, alice, bob, completedCall(10))
	events := e.eventCount()

	again := e.writeCallRecord(t, 503, alice, bob, completedCall(10))
	if again.Seq != first.Seq || first.Seq != 2 {
		t.Fatalf("retry got seq %d, first %d (want 2)", again.Seq, first.Seq)
	}
	if e.eventCount() != events {
		t.Error("retry must not emit another event")
	}
}
