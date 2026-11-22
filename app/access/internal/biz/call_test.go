package biz

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	kerrors "github.com/go-kratos/kratos/v3/errors"
	"google.golang.org/protobuf/proto"

	v1 "github.com/Mars-08042/MarsChat/api/access/v1"
	"github.com/Mars-08042/MarsChat/pkg/route"
)

// ---- 通话的假依赖 ----

// fakeCallRepo 在内存中实现 CallRepo 的语义，与 data.CallRepo 的 Lua 脚本一致（后者另有测试）。
type fakeCallRepo struct {
	mu      sync.Mutex
	calls   map[int64]*Call
	users   map[int64]int64 // 忙线标记：用户 → 通话
	touches int             // 续期了 TTL 的次数
	err     error
}

func newFakeCallRepo() *fakeCallRepo {
	return &fakeCallRepo{calls: map[int64]*Call{}, users: map[int64]int64{}}
}

func (r *fakeCallRepo) Create(_ context.Context, c *Call, _ time.Duration) (CreateResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return 0, r.err
	}
	if _, ok := r.users[c.CallerID]; ok {
		return CreateCallerBusy, nil
	}
	if _, ok := r.users[c.CalleeID]; ok {
		return CreateCalleeBusy, nil
	}
	cp := *c
	r.calls[c.ID] = &cp
	r.users[c.CallerID], r.users[c.CalleeID] = c.ID, c.ID
	return CreateOK, nil
}

func (r *fakeCallRepo) Get(_ context.Context, id int64) (*Call, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return nil, r.err
	}
	c, ok := r.calls[id]
	if !ok {
		return nil, nil
	}
	cp := *c
	return &cp, nil
}

func (r *fakeCallRepo) Accept(_ context.Context, k CallKey, connID int64, addr string, at time.Time, _ time.Duration) (bool, CallState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return false, "", r.err
	}
	c, ok := r.calls[k.ID]
	if !ok {
		return false, "", nil
	}
	if c.State != CallRinging {
		return false, c.State, nil
	}
	c.State, c.CalleeConn, c.CalleeAddr, c.AnsweredAt = CallActive, connID, addr, at
	return true, CallActive, nil
}

func (r *fakeCallRepo) End(_ context.Context, k CallKey, cond EndCond) (*Call, CallState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return nil, "", r.err
	}
	c, ok := r.calls[k.ID]
	if !ok {
		return nil, "", nil
	}
	match := true
	switch cond.By {
	case PartyCallerConn:
		match = c.CallerConn == cond.ID
	case PartyCalleeConn:
		match = c.CalleeConn == cond.ID
	case PartyCallee:
		match = c.CalleeID == cond.ID
	}
	if !match || (cond.State != "" && c.State != cond.State) {
		return nil, c.State, nil
	}
	delete(r.calls, k.ID)
	for _, uid := range []int64{k.CallerID, k.CalleeID} {
		if r.users[uid] == k.ID {
			delete(r.users, uid)
		}
	}
	return c, "", nil
}

func (r *fakeCallRepo) Touch(_ context.Context, k CallKey, connID int64, _ time.Duration) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return false, r.err
	}
	c, ok := r.calls[k.ID]
	if !ok || (c.CallerConn != connID && c.CalleeConn != connID) {
		return false, nil
	}
	if c.State == CallActive {
		r.touches++
	}
	return true, nil
}

func (r *fakeCallRepo) empty() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls) == 0 && len(r.users) == 0
}

func (r *fakeCallRepo) touchCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.touches
}

func (r *fakeCallRepo) setErr(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.err = err
}

// fakePerm 默认放行，err 非空时返回它。
type fakePerm struct {
	mu  sync.Mutex
	err error
}

func (p *fakePerm) CheckCall(context.Context, int64, int64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

func (p *fakePerm) set(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.err = err
}

type forwarded struct {
	addr    string
	connIDs []int64
	frame   *v1.ServerFrame
}

// fakeForwarder 记录发往其他实例的推送。fail 为真时调用失败，模拟实例不可达。
type fakeForwarder struct {
	mu   sync.Mutex
	got  []forwarded
	fail bool
}

func (f *fakeForwarder) Push(_ context.Context, addr string, connIDs []int64, frame *v1.ServerFrame) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, forwarded{addr, connIDs, frame})
	if f.fail {
		return 0, errors.New("instance unreachable")
	}
	return len(connIDs), nil
}

func (f *fakeForwarder) setFail(fail bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = fail
}

func (f *fakeForwarder) all() []forwarded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]forwarded(nil), f.got...)
}

// ---- 帧与断言 ----

func callFrame(rid uint64, req *v1.CallRequest) *v1.ClientFrame {
	return &v1.ClientFrame{RequestId: rid, Body: &v1.ClientFrame_Call{Call: req}}
}

func inviteFrame(rid uint64, callee int64) *v1.ClientFrame {
	return callFrame(rid, &v1.CallRequest{Action: &v1.CallRequest_Invite{Invite: &v1.CallInvite{
		CalleeId: callee, Media: v1.CallMedia_CALL_MEDIA_VIDEO,
	}}})
}

func acceptFrame(rid uint64, id int64) *v1.ClientFrame {
	return callFrame(rid, &v1.CallRequest{Action: &v1.CallRequest_Accept{Accept: &v1.CallAccept{CallId: id}}})
}

func rejectFrame(rid uint64, id int64) *v1.ClientFrame {
	return callFrame(rid, &v1.CallRequest{Action: &v1.CallRequest_Reject{Reject: &v1.CallReject{CallId: id}}})
}

func hangupFrame(rid uint64, id int64) *v1.ClientFrame {
	return callFrame(rid, &v1.CallRequest{Action: &v1.CallRequest_Hangup{Hangup: &v1.CallHangup{CallId: id}}})
}

func sdpSignal(id int64, typ v1.SdpType, sdp string) *v1.CallSignal {
	return &v1.CallSignal{CallId: id, Payload: &v1.CallSignal_Sdp{Sdp: &v1.SessionDescription{Type: typ, Sdp: sdp}}}
}

func iceSignal(id int64, candidate string) *v1.CallSignal {
	idx := uint32(0)
	return &v1.CallSignal{CallId: id, Payload: &v1.CallSignal_IceCandidate{IceCandidate: &v1.IceCandidate{
		Candidate: candidate, SdpMid: "0", SdpMlineIndex: &idx,
	}}}
}

func signalFrame(rid uint64, s *v1.CallSignal) *v1.ClientFrame {
	return callFrame(rid, &v1.CallRequest{Action: &v1.CallRequest_Signal{Signal: s}})
}

// expectAck 读下一帧，要求是 request_id 为 rid 的 CallAck，返回 call_id。
func (c *client) expectAck(rid uint64) int64 {
	c.t.Helper()
	f := c.recv()
	if f.GetCallAck() == nil || f.GetRequestId() != rid {
		c.t.Fatalf("want CallAck with request_id %d, got %v", rid, f)
	}
	return f.GetCallAck().GetCallId()
}

// expectEvent 读下一帧，要求是通话事件。
func (c *client) expectEvent() *v1.CallEvent {
	c.t.Helper()
	f := c.recv()
	if f.GetCallEvent() == nil || f.GetRequestId() != 0 {
		c.t.Fatalf("want CallEvent, got %v", f)
	}
	return f.GetCallEvent()
}

func (c *client) expectEnded(id int64, reason v1.CallEndReason) {
	c.t.Helper()
	e := c.expectEvent().GetEnded()
	if e.GetCallId() != id || e.GetReason() != reason {
		c.t.Fatalf("want CallEnded{%d, %v}, got %v", id, reason, e)
	}
}

// expectQuiet 用一次心跳确认没有积压的帧：下一帧必须是这次心跳的 Pong。
func (c *client) expectQuiet(rid uint64) {
	c.t.Helper()
	c.send(pingFrame(rid))
	f := c.recv()
	if f.GetPong() == nil || f.GetRequestId() != rid {
		c.t.Fatalf("want only Pong %d, got %v", rid, f)
	}
}

// callSetup 是一通振铃中的通话：主叫 a（用户 42），被叫 b1、b2（用户 43 的两个设备）都已收到来电。
type callSetup struct {
	e      *env
	a      *client
	b1, b2 *client
	id     int64
}

func setupRinging(t *testing.T) *callSetup {
	t.Helper()
	e := newEnv(t, nil)
	s := &callSetup{e: e, a: e.dial(t), b1: e.dial(t), b2: e.dial(t)}
	s.a.login("ok-42-7")
	s.b1.login("ok-43-8")
	s.b2.login("ok-43-9")
	s.a.send(inviteFrame(10, 43))
	s.id = s.a.expectAck(10)
	for _, b := range []*client{s.b1, s.b2} {
		in := b.expectEvent().GetIncoming()
		if in.GetCallId() != s.id || in.GetCallerId() != 42 || in.GetMedia() != v1.CallMedia_CALL_MEDIA_VIDEO {
			t.Fatalf("incoming = %v, want call %d from 42 (video)", in, s.id)
		}
	}
	return s
}

// setupActive 在振铃的基础上由 b1 接听。
func setupActive(t *testing.T) *callSetup {
	t.Helper()
	s := setupRinging(t)
	s.b1.send(acceptFrame(20, s.id))
	s.b1.expectAck(20)
	s.b2.expectEnded(s.id, v1.CallEndReason_CALL_END_REASON_ANSWERED_ELSEWHERE)
	if got := s.a.expectEvent().GetAccepted().GetCallId(); got != s.id {
		t.Fatalf("accepted call_id = %d, want %d", got, s.id)
	}
	return s
}

// ---- 测试 ----

func TestCallFullFlow(t *testing.T) {
	s := setupActive(t)

	// SDP 与 ICE 只在主叫连接和接听连接之间转发，不回显给发送方，也不发给被叫的其他设备
	offer := sdpSignal(s.id, v1.SdpType_SDP_TYPE_OFFER, "v=0 offer")
	s.a.send(signalFrame(30, offer))
	s.a.expectAck(30)
	if got := s.b1.expectEvent().GetSignal(); !proto.Equal(got, offer) {
		t.Fatalf("b1 got signal %v, want %v", got, offer)
	}
	answer := sdpSignal(s.id, v1.SdpType_SDP_TYPE_ANSWER, "v=0 answer")
	s.b1.send(signalFrame(31, answer))
	s.b1.expectAck(31)
	if got := s.a.expectEvent().GetSignal(); !proto.Equal(got, answer) {
		t.Fatalf("a got signal %v, want %v", got, answer)
	}
	ice := iceSignal(s.id, "candidate:1 1 udp 2122260223 192.0.2.1 54400 typ host")
	s.b1.send(signalFrame(32, ice))
	s.b1.expectAck(32)
	if got := s.a.expectEvent().GetSignal(); !proto.Equal(got, ice) {
		t.Fatalf("a got signal %v, want %v", got, ice)
	}
	s.a.expectQuiet(33)
	s.b1.expectQuiet(34)
	s.b2.expectQuiet(35)

	// 通话双方的心跳续期通话状态，没有接听的 b2 不续期
	if n := s.e.calls.touchCount(); n != 2 {
		t.Errorf("touches = %d, want 2 (heartbeats of a and b1)", n)
	}

	s.a.send(hangupFrame(40, s.id))
	s.a.expectAck(40)
	s.b1.expectEnded(s.id, v1.CallEndReason_CALL_END_REASON_HANGUP)
	s.b2.expectQuiet(41)
	if !s.e.calls.empty() {
		t.Error("call state left after hangup")
	}
}

func TestCallInviteErrors(t *testing.T) {
	e := newEnv(t, nil)
	a := e.dial(t)
	a.login("ok-42-7")
	b := e.dial(t)
	b.login("ok-43-8")

	// 呼叫自己、帧字段不合法
	a.send(inviteFrame(1, 42))
	wantError(t, a.recv(), 1, 400, v1.ErrorReason_INVALID_FRAME.String())
	a.send(callFrame(2, &v1.CallRequest{Action: &v1.CallRequest_Invite{Invite: &v1.CallInvite{CalleeId: 43}}}))
	wantError(t, a.recv(), 2, 400, v1.ErrorReason_INVALID_FRAME.String())
	a.send(callFrame(3, &v1.CallRequest{}))
	wantError(t, a.recv(), 3, 400, v1.ErrorReason_INVALID_FRAME.String())

	// social 的业务错误原样返回，调用失败为 503
	e.perm.set(kerrors.New(403, "NOT_FRIENDS", "对方不是你的好友"))
	a.send(inviteFrame(4, 43))
	wantError(t, a.recv(), 4, 403, "NOT_FRIENDS")
	e.perm.set(ErrUpstreamUnavailable(errors.New("social down")))
	a.send(inviteFrame(5, 43))
	wantError(t, a.recv(), 5, 503, v1.ErrorReason_UPSTREAM_UNAVAILABLE.String())
	e.perm.set(nil)

	// 被叫没有在线连接
	a.send(inviteFrame(6, 44))
	wantError(t, a.recv(), 6, 404, v1.ErrorReason_CALL_PEER_OFFLINE.String())

	// Redis 不可用
	e.calls.setErr(errors.New("redis down"))
	a.send(inviteFrame(7, 43))
	wantError(t, a.recv(), 7, 503, v1.ErrorReason_UPSTREAM_UNAVAILABLE.String())
	e.calls.setErr(nil)

	b.expectQuiet(8)
	if !e.calls.empty() {
		t.Error("call state left after failed invites")
	}
}

func TestCallBusy(t *testing.T) {
	s := setupRinging(t)
	c := s.e.dial(t)
	c.login("ok-44-10")
	a2 := s.e.dial(t)
	a2.login("ok-42-11")

	// 被叫在振铃中算忙线；主叫的其他设备也不能再发起呼叫
	c.send(inviteFrame(1, 43))
	wantError(t, c.recv(), 1, 409, v1.ErrorReason_CALL_BUSY.String())
	a2.send(inviteFrame(2, 44))
	wantError(t, a2.recv(), 2, 409, v1.ErrorReason_ALREADY_IN_CALL.String())

	// 通话结束后忙线标记释放
	s.a.send(hangupFrame(3, s.id))
	s.a.expectAck(3)
	c.send(inviteFrame(4, 43))
	c.expectAck(4)
}

func TestCallReject(t *testing.T) {
	s := setupRinging(t)
	s.b2.send(rejectFrame(1, s.id))
	s.b2.expectAck(1)
	s.a.expectEnded(s.id, v1.CallEndReason_CALL_END_REASON_REJECTED)
	s.b1.expectEnded(s.id, v1.CallEndReason_CALL_END_REASON_REJECTED)
	s.b1.send(acceptFrame(2, s.id))
	wantError(t, s.b1.recv(), 2, 404, v1.ErrorReason_CALL_NOT_FOUND.String())
	if !s.e.calls.empty() {
		t.Error("call state left after reject")
	}
}

func TestCallAlreadyAnswered(t *testing.T) {
	s := setupActive(t)
	s.b2.send(acceptFrame(1, s.id))
	wantError(t, s.b2.recv(), 1, 409, v1.ErrorReason_CALL_ALREADY_ANSWERED.String())
	s.b2.send(rejectFrame(2, s.id))
	wantError(t, s.b2.recv(), 2, 409, v1.ErrorReason_CALL_ALREADY_ANSWERED.String())
	// 不是被叫的用户不能接听
	c := s.e.dial(t)
	c.login("ok-44-10")
	c.send(acceptFrame(3, s.id))
	wantError(t, c.recv(), 3, 404, v1.ErrorReason_CALL_NOT_FOUND.String())
}

func TestCallCancelWhileRinging(t *testing.T) {
	s := setupRinging(t)
	s.a.send(hangupFrame(1, s.id))
	s.a.expectAck(1)
	s.b1.expectEnded(s.id, v1.CallEndReason_CALL_END_REASON_HANGUP)
	s.b2.expectEnded(s.id, v1.CallEndReason_CALL_END_REASON_HANGUP)
	if !s.e.calls.empty() {
		t.Error("call state left after cancel")
	}
}

func TestCallSignalAndHangupRules(t *testing.T) {
	s := setupRinging(t)
	// 接听前不转发 SDP；振铃中的被叫只能拒绝，不能挂断
	s.a.send(signalFrame(1, sdpSignal(s.id, v1.SdpType_SDP_TYPE_OFFER, "v=0")))
	wantError(t, s.a.recv(), 1, 404, v1.ErrorReason_CALL_NOT_FOUND.String())
	s.b1.send(hangupFrame(2, s.id))
	wantError(t, s.b1.recv(), 2, 404, v1.ErrorReason_CALL_NOT_FOUND.String())

	s.b1.send(acceptFrame(3, s.id))
	s.b1.expectAck(3)
	s.b2.expectEnded(s.id, v1.CallEndReason_CALL_END_REASON_ANSWERED_ELSEWHERE)
	s.a.expectEvent()
	// 没有接听的设备不能发信令
	s.b2.send(signalFrame(4, iceSignal(s.id, "")))
	wantError(t, s.b2.recv(), 4, 404, v1.ErrorReason_CALL_NOT_FOUND.String())
	// 接听连接可以挂断
	s.b1.send(hangupFrame(5, s.id))
	s.b1.expectAck(5)
	s.a.expectEnded(s.id, v1.CallEndReason_CALL_END_REASON_HANGUP)
	s.b2.expectQuiet(6)
}

func TestCallRingTimeout(t *testing.T) {
	e := newEnv(t, nil)
	e.hub.calls.ringTimeout = 200 * time.Millisecond
	a, b := e.dial(t), e.dial(t)
	a.login("ok-42-7")
	b.login("ok-43-8")
	a.send(inviteFrame(1, 43))
	id := a.expectAck(1)
	b.expectEvent()
	a.expectEnded(id, v1.CallEndReason_CALL_END_REASON_TIMEOUT)
	b.expectEnded(id, v1.CallEndReason_CALL_END_REASON_TIMEOUT)
	if !e.calls.empty() {
		t.Error("call state left after ring timeout")
	}
}

// 接听后定时器被取消，超时不会结束通话。
func TestCallRingTimeoutAfterAccept(t *testing.T) {
	e := newEnv(t, nil)
	e.hub.calls.ringTimeout = 300 * time.Millisecond
	a, b := e.dial(t), e.dial(t)
	a.login("ok-42-7")
	b.login("ok-43-8")
	a.send(inviteFrame(1, 43))
	id := a.expectAck(1)
	b.expectEvent()
	b.send(acceptFrame(2, id))
	b.expectAck(2)
	a.expectEvent()
	time.Sleep(500 * time.Millisecond)
	a.expectQuiet(3)
	b.expectQuiet(4)
	if e.calls.empty() {
		t.Error("active call ended by ring timer")
	}
}

func TestCallDisconnect(t *testing.T) {
	t.Run("接听连接断开", func(t *testing.T) {
		s := setupActive(t)
		_ = s.b1.ws.Close()
		s.a.expectEnded(s.id, v1.CallEndReason_CALL_END_REASON_DISCONNECTED)
		eventually(t, s.e.calls.empty)
	})
	t.Run("主叫在振铃中断开", func(t *testing.T) {
		s := setupRinging(t)
		_ = s.a.ws.Close()
		s.b1.expectEnded(s.id, v1.CallEndReason_CALL_END_REASON_DISCONNECTED)
		s.b2.expectEnded(s.id, v1.CallEndReason_CALL_END_REASON_DISCONNECTED)
		eventually(t, s.e.calls.empty)
	})
	t.Run("振铃中的被叫设备断开不影响通话", func(t *testing.T) {
		s := setupRinging(t)
		_ = s.b2.ws.Close()
		s.b1.send(acceptFrame(1, s.id))
		s.b1.expectAck(1)
		s.a.expectEvent()
	})
}

// 被叫在其他实例上：来电和结束经 Forwarder 推送。
func TestCallRemoteCallee(t *testing.T) {
	e := newEnv(t, nil)
	e.routes.addRemote(43, route.Entry{ConnID: 900, SessionID: 8, Addr: "10.0.0.2:19100"})
	a := e.dial(t)
	a.login("ok-42-7")
	a.send(inviteFrame(1, 43))
	id := a.expectAck(1)
	a.send(hangupFrame(2, id))
	a.expectAck(2)

	// 挂断先回复 CallAck 再转发结束事件，收到确认时转发可能还没发出
	eventually(t, func() bool { return len(e.fwd.all()) >= 2 })
	got := e.fwd.all()
	if len(got) != 2 {
		t.Fatalf("forwarded %d frames, want 2 (incoming, ended)", len(got))
	}
	for _, f := range got {
		if f.addr != "10.0.0.2:19100" || len(f.connIDs) != 1 || f.connIDs[0] != 900 {
			t.Errorf("forwarded to %s %v, want 10.0.0.2:19100 [900]", f.addr, f.connIDs)
		}
	}
	if got[0].frame.GetCallEvent().GetIncoming().GetCallId() != id {
		t.Errorf("first forwarded frame = %v, want incoming", got[0].frame)
	}
	if r := got[1].frame.GetCallEvent().GetEnded().GetReason(); r != v1.CallEndReason_CALL_END_REASON_HANGUP {
		t.Errorf("second forwarded frame reason = %v, want HANGUP", r)
	}
}

// 被叫的路由都已失效（实例不可达）：先回复 CallAck，再以 PEER_OFFLINE 结束。
func TestCallInviteAllPushesFail(t *testing.T) {
	e := newEnv(t, nil)
	e.routes.addRemote(43, route.Entry{ConnID: 900, SessionID: 8, Addr: "10.0.0.2:19100"})
	e.fwd.setFail(true)
	a := e.dial(t)
	a.login("ok-42-7")
	a.send(inviteFrame(1, 43))
	id := a.expectAck(1)
	a.expectEnded(id, v1.CallEndReason_CALL_END_REASON_PEER_OFFLINE)
	if !e.calls.empty() {
		t.Error("call state left after peer offline")
	}
	// 忙线标记已释放，可以再次呼叫
	e.fwd.setFail(false)
	a.send(inviteFrame(2, 43))
	a.expectAck(2)
}

// 转发信令时对方实例不可达：请求已经回复 CallAck，随后按断线结束，发送方也收到 CallEnded。
func TestCallSignalPeerUnreachable(t *testing.T) {
	e := newEnv(t, nil)
	e.routes.addRemote(43, route.Entry{ConnID: 900, SessionID: 8, Addr: "10.0.0.2:19100"})
	a := e.dial(t)
	a.login("ok-42-7")
	a.send(inviteFrame(1, 43))
	id := a.expectAck(1)
	// 邀请先回复 CallAck 再转发来电：等来电转发完再让实例不可达，否则来电转发失败，邀请会按对方离线结束
	eventually(t, func() bool { return len(e.fwd.all()) == 1 })
	// 模拟其他实例上的 900 接听
	call, _ := e.calls.Get(context.Background(), id)
	if ok, _, err := e.calls.Accept(context.Background(), call.CallKey, 900, "10.0.0.2:19100", time.Now(), time.Minute); !ok || err != nil {
		t.Fatalf("accept = %v, %v", ok, err)
	}

	e.fwd.setFail(true)
	a.send(signalFrame(2, sdpSignal(id, v1.SdpType_SDP_TYPE_OFFER, "v=0")))
	a.expectAck(2)
	a.expectEnded(id, v1.CallEndReason_CALL_END_REASON_DISCONNECTED)
	if !e.calls.empty() {
		t.Error("call state left after peer unreachable")
	}
	// 连接上的通话记录已清掉
	a.send(hangupFrame(3, id))
	wantError(t, a.recv(), 3, 404, v1.ErrorReason_CALL_NOT_FOUND.String())
}

// 对方挂断后，本连接的下一次心跳发现通话已不存在，不再续期。
func TestCallHeartbeatClearsEndedCall(t *testing.T) {
	s := setupActive(t)
	s.b1.send(hangupFrame(1, s.id))
	s.b1.expectAck(1)
	s.a.expectEnded(s.id, v1.CallEndReason_CALL_END_REASON_HANGUP)
	s.a.expectQuiet(2)
	s.a.expectQuiet(3)
	if n := s.e.calls.touchCount(); n != 0 {
		t.Errorf("touches = %d, want 0 after the call ended", n)
	}
}
