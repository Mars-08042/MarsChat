//go:build e2e

package e2e

import (
	"context"
	"net/http"
	"testing"

	"google.golang.org/protobuf/proto"

	accessv1 "github.com/Mars-08042/MarsChat/api/access/v1"
	socialv1 "github.com/Mars-08042/MarsChat/api/social/v1"
	"github.com/Mars-08042/MarsChat/test/testkit"
)

// callStateGone 检查通话结束后 Redis 中的通话状态和双方的忙线标记都已删除。
func callStateGone(t *testing.T, callID int64, users ...int64) {
	t.Helper()
	rdb := newRedisClient(t)
	keys := []string{"access:call:" + id(callID)}
	for _, u := range users {
		keys = append(keys, "access:call_user:"+id(u))
	}
	n, err := rdb.Exists(context.Background(), keys...).Result()
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d of %v still exist after the call ended", n, keys)
	}
}

// 完整流程：多端响铃，一端接听后另一端停止响铃；SDP 与 ICE 只在主叫连接和接听连接之间转发，不回显。
func TestCallSignaling(t *testing.T) {
	resetRateLimits(t)
	a, b := newUser(t), newUser(t)
	makeFriends(t, a, b)
	a1, b1, b2 := dialWS(t), dialWS(t), dialWS(t)
	a1.Auth(a.token)
	b1.Auth(b.token)
	b2.Auth(b.token)

	callID := a1.MustInvite(1, b.id)
	for _, c := range []*wsClient{b1, b2} {
		in := c.WaitFrame("incoming call", testkit.IsIncoming(callID)).GetCallEvent().GetIncoming()
		if in.GetCallerId() != a.id || in.GetMedia() != accessv1.CallMedia_CALL_MEDIA_VIDEO {
			t.Fatalf("incoming = %v", in)
		}
	}

	b1.MustAck(b1.CallOp(2, testkit.AcceptReq(callID)))
	b2.WaitFrame("answered elsewhere", testkit.IsCallEnded(callID, accessv1.CallEndReason_CALL_END_REASON_ANSWERED_ELSEWHERE))
	a1.WaitFrame("accepted", testkit.IsCallAccepted(callID))

	offer := testkit.SDP(callID, accessv1.SdpType_SDP_TYPE_OFFER, "v=0 offer")
	a1.MustAck(a1.CallOp(3, testkit.SignalReq(offer)))
	if got := b1.WaitFrame("offer", testkit.IsCallSignal(callID)).GetCallEvent().GetSignal(); !proto.Equal(got, offer) {
		t.Fatalf("offer = %v", got)
	}
	answer := testkit.SDP(callID, accessv1.SdpType_SDP_TYPE_ANSWER, "v=0 answer")
	b1.MustAck(b1.CallOp(4, testkit.SignalReq(answer)))
	if got := a1.WaitFrame("answer", testkit.IsCallSignal(callID)).GetCallEvent().GetSignal(); !proto.Equal(got, answer) {
		t.Fatalf("answer = %v", got)
	}
	ice := testkit.ICE(callID, "candidate:1 1 udp 2122260223 192.0.2.1 54400 typ host")
	a1.MustAck(a1.CallOp(5, testkit.SignalReq(ice)))
	if got := b1.WaitFrame("ice", testkit.IsCallSignal(callID)).GetCallEvent().GetSignal(); !proto.Equal(got, ice) {
		t.Fatalf("ice = %v", got)
	}
	a1.ExpectNoCallEvent(6)
	b2.ExpectNoCallEvent(7)

	// 通话中心跳续期，忙线标记随通话一起续期
	a1.Ping(8)
	if ttl := newRedisClient(t).PTTL(context.Background(), "access:call_user:"+id(b.id)).Val(); ttl <= 0 {
		t.Fatalf("busy marker TTL = %v", ttl)
	}

	a1.MustAck(a1.CallOp(9, testkit.HangupReq(callID)))
	b1.WaitFrame("hangup", testkit.IsCallEnded(callID, accessv1.CallEndReason_CALL_END_REASON_HANGUP))
	callStateGone(t, callID, a.id, b.id)
}

func TestCallErrors(t *testing.T) {
	resetRateLimits(t)
	a, b, c, stranger, offline := newUser(t), newUser(t), newUser(t), newUser(t), newUser(t)
	makeFriends(t, a, b)
	makeFriends(t, c, b)
	makeFriends(t, a, c)
	makeFriends(t, a, offline)
	a1, a2, b1, c1 := dialWS(t), dialWS(t), dialWS(t), dialWS(t)
	a1.Auth(a.token)
	a2.Auth(a.token)
	b1.Auth(b.token)
	c1.Auth(c.token)

	// 权限错误原样来自 social
	wantFrameError(t, a1.Invite(1, stranger.id), 403, "NOT_FRIENDS")
	wantFrameError(t, a1.Invite(2, a.id), 400, accessv1.ErrorReason_INVALID_FRAME.String())
	wantFrameError(t, a1.Invite(3, offline.id), 404, accessv1.ErrorReason_CALL_PEER_OFFLINE.String())
	mustSocial(t, http.MethodPost, "/v1/blocks", c.token, &socialv1.BlockUserRequest{UserId: a.id}, nil)
	wantFrameError(t, a1.Invite(4, c.id), 403, "BLOCKED")
	mustSocial(t, http.MethodDelete, "/v1/blocks/"+id(a.id), c.token, nil, nil)

	// 振铃中：被叫忙线，主叫的其他设备也不能再发起
	callID := a1.MustInvite(5, b.id)
	b1.WaitFrame("incoming call", testkit.IsIncoming(callID))
	wantFrameError(t, c1.Invite(6, b.id), 409, accessv1.ErrorReason_CALL_BUSY.String())
	wantFrameError(t, a2.Invite(7, c.id), 409, accessv1.ErrorReason_ALREADY_IN_CALL.String())
	// 接听前不能发信令，不是被叫不能接听
	wantFrameError(t, a1.CallOp(8, testkit.SignalReq(testkit.SDP(callID, accessv1.SdpType_SDP_TYPE_OFFER, "v=0"))),
		404, accessv1.ErrorReason_CALL_NOT_FOUND.String())
	wantFrameError(t, c1.CallOp(9, testkit.AcceptReq(callID)), 404, accessv1.ErrorReason_CALL_NOT_FOUND.String())

	// 拒绝后通话结束，忙线标记释放
	b1.MustAck(b1.CallOp(10, testkit.RejectReq(callID)))
	a1.WaitFrame("rejected", testkit.IsCallEnded(callID, accessv1.CallEndReason_CALL_END_REASON_REJECTED))
	callStateGone(t, callID, a.id, b.id)
	next := c1.MustInvite(11, b.id)
	b1.WaitFrame("incoming call", testkit.IsIncoming(next))
	c1.MustAck(c1.CallOp(12, testkit.HangupReq(next)))
	b1.WaitFrame("cancelled", testkit.IsCallEnded(next, accessv1.CallEndReason_CALL_END_REASON_HANGUP))
}

// 信令连接断开时结束通话，对方收到 DISCONNECTED。
func TestCallEndsOnDisconnect(t *testing.T) {
	resetRateLimits(t)
	a, b := newUser(t), newUser(t)
	makeFriends(t, a, b)
	a1, b1 := dialWS(t), dialWS(t)
	a1.Auth(a.token)
	b1.Auth(b.token)

	callID := a1.MustInvite(1, b.id)
	b1.WaitFrame("incoming call", testkit.IsIncoming(callID))
	b1.MustAck(b1.CallOp(2, testkit.AcceptReq(callID)))
	a1.WaitFrame("accepted", testkit.IsCallAccepted(callID))

	_ = b1.Conn.Close()
	a1.WaitFrame("disconnected", testkit.IsCallEnded(callID, accessv1.CallEndReason_CALL_END_REASON_DISCONNECTED))
	callStateGone(t, callID, a.id, b.id)
}
