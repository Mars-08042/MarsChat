package service

import (
	"testing"

	"buf.build/go/protovalidate"

	v1 "github.com/Mars-08042/MarsChat/api/message/v1"
)

// 通话记录只能经 WriteCallRecord 由服务端写入：客户端的发送请求（经 access 转成 SendMessage）带通话记录时，
// 校验中间件按 proto 中的规则拒绝。
func TestSendMessageRejectsCallContent(t *testing.T) {
	req := &v1.SendMessageRequest{SenderId: 1, SessionId: 2, ConnectionId: 3, Message: &v1.OutgoingMessage{
		PeerType:    v1.ConversationType_CONVERSATION_TYPE_SINGLE,
		PeerId:      4,
		ClientMsgId: "c1",
		Content: &v1.MessageContent{Body: &v1.MessageContent_Call{Call: &v1.CallContent{
			Media: v1.CallRecordMedia_CALL_RECORD_MEDIA_AUDIO, Result: v1.CallResult_CALL_RESULT_COMPLETED, DurationSeconds: 9999,
		}}},
	}}
	if err := protovalidate.Validate(req); err == nil {
		t.Fatal("call content from a client must be rejected")
	}
	req.Message.Content = &v1.MessageContent{Body: &v1.MessageContent_Text{Text: &v1.TextContent{Text: "hi"}}}
	if err := protovalidate.Validate(req); err != nil {
		t.Fatalf("text message rejected: %v", err)
	}
}

func TestWriteCallRecordRequestValidation(t *testing.T) {
	valid := func() *v1.WriteCallRecordRequest {
		return &v1.WriteCallRecordRequest{CallId: 1, CallerId: 2, CalleeId: 3, Call: &v1.CallContent{
			Media: v1.CallRecordMedia_CALL_RECORD_MEDIA_VIDEO, Result: v1.CallResult_CALL_RESULT_CANCELLED,
		}}
	}
	if err := protovalidate.Validate(valid()); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	cases := map[string]func(r *v1.WriteCallRecordRequest){
		"same parties":   func(r *v1.WriteCallRecordRequest) { r.CalleeId = r.CallerId },
		"missing call":   func(r *v1.WriteCallRecordRequest) { r.Call = nil },
		"no media":       func(r *v1.WriteCallRecordRequest) { r.Call.Media = v1.CallRecordMedia_CALL_RECORD_MEDIA_UNSPECIFIED },
		"no result":      func(r *v1.WriteCallRecordRequest) { r.Call.Result = v1.CallResult_CALL_RESULT_UNSPECIFIED },
		"negative time":  func(r *v1.WriteCallRecordRequest) { r.Call.DurationSeconds = -1 },
		"missing callID": func(r *v1.WriteCallRecordRequest) { r.CallId = 0 },
	}
	for name, mutate := range cases {
		r := valid()
		mutate(r)
		if err := protovalidate.Validate(r); err == nil {
			t.Errorf("%s: want validation error", name)
		}
	}
}
