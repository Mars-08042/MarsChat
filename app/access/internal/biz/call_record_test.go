package biz

import (
	"errors"
	"testing"
	"time"

	kerrors "github.com/go-kratos/kratos/v3/errors"
	"google.golang.org/protobuf/proto"

	v1 "github.com/Mars-08042/MarsChat/api/access/v1"
	messagev1 "github.com/Mars-08042/MarsChat/api/message/v1"
)

func TestCallRecordMapping(t *testing.T) {
	start := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	answered := start.Add(5 * time.Second)
	video := &Call{Media: v1.CallMedia_CALL_MEDIA_VIDEO, CreatedAt: start, AnsweredAt: answered}
	ringing := &Call{Media: v1.CallMedia_CALL_MEDIA_AUDIO, CreatedAt: start}
	audio := messagev1.CallRecordMedia_CALL_RECORD_MEDIA_AUDIO
	cases := []struct {
		name   string
		call   *Call
		reason v1.CallEndReason
		ended  time.Time
		want   *messagev1.CallContent
	}{
		{"接听后挂断，时长四舍五入", video, v1.CallEndReason_CALL_END_REASON_HANGUP, answered.Add(83600 * time.Millisecond),
			&messagev1.CallContent{Media: messagev1.CallRecordMedia_CALL_RECORD_MEDIA_VIDEO, Result: messagev1.CallResult_CALL_RESULT_COMPLETED, DurationSeconds: 84}},
		{"接听后连接断开也算通话完成", video, v1.CallEndReason_CALL_END_REASON_DISCONNECTED, answered.Add(10400 * time.Millisecond),
			&messagev1.CallContent{Media: messagev1.CallRecordMedia_CALL_RECORD_MEDIA_VIDEO, Result: messagev1.CallResult_CALL_RESULT_COMPLETED, DurationSeconds: 10}},
		{"振铃中主叫挂断", ringing, v1.CallEndReason_CALL_END_REASON_HANGUP, start.Add(time.Second),
			&messagev1.CallContent{Media: audio, Result: messagev1.CallResult_CALL_RESULT_CANCELLED}},
		{"振铃中连接断开", ringing, v1.CallEndReason_CALL_END_REASON_DISCONNECTED, start.Add(time.Second),
			&messagev1.CallContent{Media: audio, Result: messagev1.CallResult_CALL_RESULT_FAILED}},
		{"被叫拒绝", ringing, v1.CallEndReason_CALL_END_REASON_REJECTED, start.Add(time.Second),
			&messagev1.CallContent{Media: audio, Result: messagev1.CallResult_CALL_RESULT_REJECTED}},
		{"无人接听", ringing, v1.CallEndReason_CALL_END_REASON_TIMEOUT, start.Add(time.Minute),
			&messagev1.CallContent{Media: audio, Result: messagev1.CallResult_CALL_RESULT_MISSED}},
		{"被叫不在线", ringing, v1.CallEndReason_CALL_END_REASON_PEER_OFFLINE, start,
			&messagev1.CallContent{Media: audio, Result: messagev1.CallResult_CALL_RESULT_FAILED}},
		{"在其他设备接听不写记录", ringing, v1.CallEndReason_CALL_END_REASON_ANSWERED_ELSEWHERE, start, nil},
		{"未知原因不写记录", ringing, v1.CallEndReason_CALL_END_REASON_UNSPECIFIED, start, nil},
	}
	for _, tc := range cases {
		if got := callRecord(tc.call, tc.reason, tc.ended); !proto.Equal(got, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// 每通通话结束时写一条记录：主叫、被叫与通话 ID 来自通话状态；被叫其他设备上的 ANSWERED_ELSEWHERE 不写。
func TestCallRecordWrittenOnEnd(t *testing.T) {
	s := setupActive(t)
	s.a.send(hangupFrame(40, s.id))
	s.a.expectAck(40)
	s.b1.expectEnded(s.id, v1.CallEndReason_CALL_END_REASON_HANGUP)

	calls := s.e.records.waitRecords(t, 1)
	r := calls[0]
	if r.callID != s.id || r.callerID != 42 || r.calleeID != 43 ||
		r.content.GetResult() != messagev1.CallResult_CALL_RESULT_COMPLETED || r.content.GetMedia() != messagev1.CallRecordMedia_CALL_RECORD_MEDIA_VIDEO {
		t.Fatalf("record = %+v", r)
	}
	time.Sleep(50 * time.Millisecond)
	if n := len(s.e.records.snapshot()); n != 1 {
		t.Errorf("record writes = %d, want 1", n)
	}
}

func TestCallRecordCancelAndReject(t *testing.T) {
	s := setupRinging(t)
	s.a.send(hangupFrame(1, s.id))
	s.a.expectAck(1)
	if got := s.e.records.waitRecords(t, 1)[0].content.GetResult(); got != messagev1.CallResult_CALL_RESULT_CANCELLED {
		t.Errorf("cancel recorded as %v", got)
	}

	s = setupRinging(t)
	s.b2.send(rejectFrame(1, s.id))
	s.b2.expectAck(1)
	if got := s.e.records.waitRecords(t, 1)[0].content.GetResult(); got != messagev1.CallResult_CALL_RESULT_REJECTED {
		t.Errorf("reject recorded as %v", got)
	}
}

// message 不可用时重试到成功；参数错误（4xx）重试也不会成功，只调用一次。
func TestCallRecordRetry(t *testing.T) {
	s := setupRinging(t)
	s.e.records.errs = []error{ErrUpstreamUnavailable(errors.New("timeout")), errors.New("connection reset")}
	s.a.send(hangupFrame(1, s.id))
	s.a.expectAck(1)
	calls := s.e.records.waitRecords(t, 3)
	if calls[0].callID != calls[2].callID {
		t.Fatalf("retries must write the same call: %+v", calls)
	}

	s = setupRinging(t)
	s.e.records.errs = []error{kerrors.BadRequest("VALIDATOR", "invalid")}
	s.a.send(hangupFrame(1, s.id))
	s.a.expectAck(1)
	s.e.records.waitRecords(t, 1)
	time.Sleep(50 * time.Millisecond)
	if n := len(s.e.records.snapshot()); n != 1 {
		t.Errorf("record writes after 4xx = %d, want 1", n)
	}
}

// 停机时不再等待下一次重试：stop 等进行中的那次调用结束就返回。
func TestCallRecordStopAbortsRetry(t *testing.T) {
	s := setupRinging(t)
	calls := s.e.hub.calls
	calls.recordRetry = []time.Duration{time.Hour}
	s.e.records.errs = []error{ErrUpstreamUnavailable(errors.New("timeout"))}
	s.a.send(hangupFrame(1, s.id))
	s.a.expectAck(1)
	s.e.records.waitRecords(t, 1)

	done := make(chan struct{})
	go func() {
		calls.stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stop did not return while a retry was pending")
	}
	if n := len(s.e.records.snapshot()); n != 1 {
		t.Errorf("record writes = %d, want 1", n)
	}
}
