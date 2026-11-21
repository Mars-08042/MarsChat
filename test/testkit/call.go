package testkit

import (
	accessv1 "github.com/Mars-08042/MarsChat/api/access/v1"
)

// CallOp 发送一个通话请求，返回同一 request_id 的 CallAck 或 Error，期间的推送被丢弃。
func (c *WS) CallOp(rid uint64, req *accessv1.CallRequest) *accessv1.ServerFrame {
	c.t.Helper()
	return c.Call(&accessv1.ClientFrame{RequestId: rid, Body: &accessv1.ClientFrame_Call{Call: req}})
}

// Invite 发起视频呼叫。
func (c *WS) Invite(rid uint64, callee int64) *accessv1.ServerFrame {
	c.t.Helper()
	return c.CallOp(rid, &accessv1.CallRequest{Action: &accessv1.CallRequest_Invite{Invite: &accessv1.CallInvite{
		CalleeId: callee, Media: accessv1.CallMedia_CALL_MEDIA_VIDEO,
	}}})
}

// MustInvite 发起呼叫并返回 call_id，没有收到 CallAck 时终止测试。
func (c *WS) MustInvite(rid uint64, callee int64) int64 {
	c.t.Helper()
	r := c.Invite(rid, callee)
	if r.GetCallAck() == nil {
		c.t.Fatalf("invite: %v", r)
	}
	return r.GetCallAck().GetCallId()
}

// AcceptReq、RejectReq、HangupReq、SignalReq 构造通话请求。
func AcceptReq(id int64) *accessv1.CallRequest {
	return &accessv1.CallRequest{Action: &accessv1.CallRequest_Accept{Accept: &accessv1.CallAccept{CallId: id}}}
}

func RejectReq(id int64) *accessv1.CallRequest {
	return &accessv1.CallRequest{Action: &accessv1.CallRequest_Reject{Reject: &accessv1.CallReject{CallId: id}}}
}

func HangupReq(id int64) *accessv1.CallRequest {
	return &accessv1.CallRequest{Action: &accessv1.CallRequest_Hangup{Hangup: &accessv1.CallHangup{CallId: id}}}
}

func SignalReq(s *accessv1.CallSignal) *accessv1.CallRequest {
	return &accessv1.CallRequest{Action: &accessv1.CallRequest_Signal{Signal: s}}
}

// SDP 构造 SDP 信令。
func SDP(id int64, typ accessv1.SdpType, sdp string) *accessv1.CallSignal {
	return &accessv1.CallSignal{CallId: id, Payload: &accessv1.CallSignal_Sdp{Sdp: &accessv1.SessionDescription{Type: typ, Sdp: sdp}}}
}

// ICE 构造 ICE 候选信令。
func ICE(id int64, candidate string) *accessv1.CallSignal {
	idx := uint32(0)
	return &accessv1.CallSignal{CallId: id, Payload: &accessv1.CallSignal_IceCandidate{IceCandidate: &accessv1.IceCandidate{
		Candidate: candidate, SdpMid: "0", SdpMlineIndex: &idx,
	}}}
}

// MustAck 要求响应是 CallAck。
func (c *WS) MustAck(r *accessv1.ServerFrame) {
	c.t.Helper()
	if r.GetCallAck() == nil {
		c.t.Fatalf("want CallAck, got %v", r)
	}
}

// IsIncoming 匹配来电推送。
func IsIncoming(id int64) func(*accessv1.ServerFrame) bool {
	return func(f *accessv1.ServerFrame) bool {
		return f.GetCallEvent().GetIncoming().GetCallId() == id && id != 0
	}
}

// IsCallAccepted 匹配“对方已接听”推送。
func IsCallAccepted(id int64) func(*accessv1.ServerFrame) bool {
	return func(f *accessv1.ServerFrame) bool {
		return f.GetCallEvent().GetAccepted().GetCallId() == id && id != 0
	}
}

// IsCallEnded 匹配通话结束推送。
func IsCallEnded(id int64, reason accessv1.CallEndReason) func(*accessv1.ServerFrame) bool {
	return func(f *accessv1.ServerFrame) bool {
		e := f.GetCallEvent().GetEnded()
		return e != nil && e.GetCallId() == id && e.GetReason() == reason
	}
}

// IsCallSignal 匹配对方转发来的信令。
func IsCallSignal(id int64) func(*accessv1.ServerFrame) bool {
	return func(f *accessv1.ServerFrame) bool {
		s := f.GetCallEvent().GetSignal()
		return s != nil && s.GetCallId() == id
	}
}

// ExpectNoCallEvent 发送 Ping 并读到 Pong 为止，期间不能收到通话事件。用法与 ExpectNoMessage 相同：
// 先确认另一个接收者已经收到同一批推送，再调用它。
func (c *WS) ExpectNoCallEvent(rid uint64) {
	c.t.Helper()
	c.Send(&accessv1.ClientFrame{RequestId: rid, Body: &accessv1.ClientFrame_Ping{Ping: &accessv1.Ping{}}})
	for {
		f := c.Recv()
		if e := f.GetCallEvent(); e != nil {
			c.t.Fatalf("unexpected call event: %v", e)
		}
		if f.GetPong() != nil && f.GetRequestId() == rid {
			return
		}
	}
}
