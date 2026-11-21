//go:build cluster

package cluster

import (
	"strconv"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	accessv1 "github.com/Mars-08042/MarsChat/api/access/v1"
	"github.com/Mars-08042/MarsChat/test/testkit"
)

// callStateGone 检查通话结束后 Redis 中的通话状态和双方的忙线标记都已删除。
func callStateGone(t *testing.T, callID int64, users ...int64) {
	t.Helper()
	keys := []string{"access:call:" + strconv.FormatInt(callID, 10)}
	for _, u := range users {
		keys = append(keys, "access:call_user:"+strconv.FormatInt(u, 10))
	}
	n, err := testkit.NewRedis(t).Exists(ctx(t), keys...).Result()
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d of %v still exist after the call ended", n, keys)
	}
}

// 主叫与接听设备在不同的 access 实例上，信令经 access 之间直连转发；被叫的另一台设备与主叫在同一实例上，
// 它收到的“已在其他设备接听”由接听设备所在的实例转发过来。
func TestCallAcrossInstances(t *testing.T) {
	resetRateLimits(t)
	a, b := newUser(t), newUser(t)
	makeFriends(t, a, b)
	a1 := connect(t, a)
	b1 := connectWhere(t, b, "an instance other than "+a1.addr, func(addr string) bool { return addr != a1.addr })
	b2 := connectWhere(t, b, a1.addr, func(addr string) bool { return addr == a1.addr })
	t.Logf("a1 and b2 on %s, b1 on %s", a1.addr, b1.addr)

	start := time.Now()
	callID := a1.MustInvite(1, b.id)
	b1.WaitFrame("incoming call on the other instance", testkit.IsIncoming(callID))
	incoming := time.Since(start)
	b2.WaitFrame("incoming call on the same instance", testkit.IsIncoming(callID))

	b1.MustAck(b1.CallOp(2, testkit.AcceptReq(callID)))
	a1.WaitFrame("accepted across instances", testkit.IsCallAccepted(callID))
	b2.WaitFrame("answered elsewhere", testkit.IsCallEnded(callID, accessv1.CallEndReason_CALL_END_REASON_ANSWERED_ELSEWHERE))

	offer := testkit.SDP(callID, accessv1.SdpType_SDP_TYPE_OFFER, "v=0 offer")
	start = time.Now()
	a1.MustAck(a1.CallOp(3, testkit.SignalReq(offer)))
	got := b1.WaitFrame("offer across instances", testkit.IsCallSignal(callID)).GetCallEvent().GetSignal()
	forward := time.Since(start)
	if !proto.Equal(got, offer) {
		t.Fatalf("offer = %v", got)
	}
	answer := testkit.SDP(callID, accessv1.SdpType_SDP_TYPE_ANSWER, "v=0 answer")
	b1.MustAck(b1.CallOp(4, testkit.SignalReq(answer)))
	if got := a1.WaitFrame("answer across instances", testkit.IsCallSignal(callID)).GetCallEvent().GetSignal(); !proto.Equal(got, answer) {
		t.Fatalf("answer = %v", got)
	}
	a1.ExpectNoCallEvent(5)
	b2.ExpectNoCallEvent(6)

	b1.MustAck(b1.CallOp(7, testkit.HangupReq(callID)))
	a1.WaitFrame("hangup across instances", testkit.IsCallEnded(callID, accessv1.CallEndReason_CALL_END_REASON_HANGUP))
	callStateGone(t, callID, a.id, b.id)
	t.Logf("invite to incoming on the other instance %v; offer from send to arrival %v", incoming, forward)
}

// 通话中杀掉接听方所在的 access：主叫下一次发信令时转发失败，通话按断线结束，忙线标记释放；
// 被叫重连到存活的实例后可以再次被呼叫。
func TestFaultKillAccessDuringCall(t *testing.T) {
	skipShort(t)
	resetRateLimits(t)
	a, b := newUser(t), newUser(t)
	makeFriends(t, a, b)
	a1 := connect(t, a)
	b1 := connectWhere(t, b, "an instance other than "+a1.addr, func(addr string) bool { return addr != a1.addr })
	victim := containerByIP(t, "access", hostOf(b1.addr))
	restoreService(t, victim, "access")

	callID := a1.MustInvite(1, b.id)
	b1.WaitFrame("incoming call", testkit.IsIncoming(callID))
	b1.MustAck(b1.CallOp(2, testkit.AcceptReq(callID)))
	a1.WaitFrame("accepted", testkit.IsCallAccepted(callID))

	killedAt := time.Now()
	docker(t, victim, "kill")
	sent := time.Now()
	a1.MustAck(a1.CallOp(3, testkit.SignalReq(testkit.ICE(callID, "candidate:1 1 udp 1 192.0.2.1 1 typ host"))))
	a1.WaitFrame("disconnected after forwarding failed", testkit.IsCallEnded(callID, accessv1.CallEndReason_CALL_END_REASON_DISCONNECTED))
	ended := time.Since(sent)
	callStateGone(t, callID, a.id, b.id)

	// 被叫经网关重连：Consul 摘除被杀实例之前，网关可能还会把连接转给它
	var b2 conn
	deadline := time.Now().Add(60 * time.Second)
	for {
		if c, err := testkit.DialE(t, gatewayWS); err == nil {
			b2 = authAndLocate(t, c, b)
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("b could not reconnect within 60s")
		}
		time.Sleep(500 * time.Millisecond)
	}
	next := a1.MustInvite(4, b.id)
	b2.WaitFrame("incoming call after reconnect", testkit.IsIncoming(next))
	a1.MustAck(a1.CallOp(5, testkit.HangupReq(next)))
	b2.WaitFrame("cancelled", testkit.IsCallEnded(next, accessv1.CallEndReason_CALL_END_REASON_HANGUP))
	t.Logf("kill %s: signal sent %v after kill, call ended %v after the signal; b reconnected to %s",
		victim, sent.Sub(killedAt).Round(time.Millisecond), ended.Round(time.Millisecond), b2.addr)
}
