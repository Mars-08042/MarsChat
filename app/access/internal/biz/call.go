package biz

import (
	"context"
	"log/slog"
	"math"
	"math/rand/v2"
	"sync"
	"time"

	"buf.build/go/protovalidate"
	kerrors "github.com/go-kratos/kratos/v3/errors"

	v1 "github.com/Mars-08042/MarsChat/api/access/v1"
	"github.com/Mars-08042/MarsChat/app/access/internal/conf"
	"github.com/Mars-08042/MarsChat/pkg/route"
)

// CallState 是通话状态。通话结束即删除，没有“已结束”状态。
type CallState string

const (
	CallRinging CallState = "ringing"
	CallActive  CallState = "active"
)

// CallKey 定位一通通话，以及主叫、被叫两人的忙线标记。主叫和被叫在通话创建后不会变。
type CallKey struct {
	ID       int64
	CallerID int64
	CalleeID int64
}

// Call 是 Redis 中的通话状态。
type Call struct {
	CallKey
	CallerConn int64
	CallerAddr string
	// 接听后才有值
	CalleeConn int64
	CalleeAddr string
	Media      v1.CallMedia
	State      CallState
	CreatedAt  time.Time
	AnsweredAt time.Time
}

// CallParty 指定结束通话时要求谁是发起方，与状态一起在 Redis 中原子判断。
type CallParty int

const (
	// PartyAny 不检查发起方，用于振铃超时
	PartyAny CallParty = iota
	// PartyCallerConn 要求发起方是主叫连接
	PartyCallerConn
	// PartyCalleeConn 要求发起方是接听连接
	PartyCalleeConn
	// PartyCallee 要求发起方是被叫用户（任一设备），用于拒绝
	PartyCallee
)

// EndCond 是结束通话的前提条件。
type EndCond struct {
	By CallParty
	// 连接 ID 或用户 ID，取决于 By
	ID int64
	// 非空时要求当前状态相同
	State CallState
}

// CreateResult 是创建通话的结果。
type CreateResult int

const (
	CreateOK CreateResult = iota
	// 主叫已在通话或振铃中
	CreateCallerBusy
	// 被叫已在通话或振铃中
	CreateCalleeBusy
)

// CallRepo 维护 Redis 中的通话状态与忙线标记，每个方法都是一次原子操作。
// 返回的错误都表示 Redis 不可用或数据损坏，业务上的不满足用返回值表达。
type CallRepo interface {
	// Create 创建振铃中的通话，同时占用双方的忙线标记，三个键的 TTL 为 ttl
	Create(ctx context.Context, c *Call, ttl time.Duration) (CreateResult, error)
	// Get 返回通话，不存在时返回 nil、nil
	Get(ctx context.Context, id int64) (*Call, error)
	// Accept 在通话处于振铃中时改为通话中，记下接听连接，三个键的 TTL 改为 ttl。
	// 返回是否成功；失败时 current 为当前状态，通话不存在时为空
	Accept(ctx context.Context, k CallKey, connID int64, addr string, at time.Time, ttl time.Duration) (ok bool, current CallState, err error)
	// End 在满足条件时删除通话，并删除仍指向它的忙线标记，返回删除前的通话。
	// 条件不满足时返回 nil 和当前状态，通话不存在时状态为空
	End(ctx context.Context, k CallKey, cond EndCond) (ended *Call, current CallState, err error)
	// Touch 返回 connID 是否仍是这通通话的主叫连接或接听连接；处于通话中时同时把三个键的 TTL 延长到 ttl
	Touch(ctx context.Context, k CallKey, connID int64, ttl time.Duration) (alive bool, err error)
}

const (
	// 一次通话操作（包括权限校验、读写 Redis、转发）的总时限，各依赖另有自己的单次超时
	callOpTimeout = 10 * time.Second
	// 振铃中状态的 TTL 比振铃超时多出的部分：正常情况下由定时器提前结束，只在主叫所在实例崩溃时兜底
	ringingTTLMargin = 15 * time.Second
)

// callRef 是连接参与的通话，只由连接的读协程访问。对方结束通话时不会清掉，所以使用前都要经 Redis 校验。
type callRef struct {
	key    CallKey
	caller bool
}

// Calls 处理通话信令（docs/design/call-signaling.md）。通话状态在 Redis 中，所有 access 实例共享；
// 发给其他实例上的连接经 Forwarder 直连推送，本实例的连接直接放进发送队列。
// 方法在各连接的读协程中调用，振铃超时在定时器协程中处理。
type Calls struct {
	ringTimeout time.Duration
	ringingTTL  time.Duration
	// 通话中状态的 TTL，等于心跳超时，双方连接每次心跳续期
	activeTTL time.Duration
	repo      CallRepo
	perm      CallPermission
	routes    RouteRepo
	fwd       Forwarder
	// 邀请和接听的 CallAck 带上的 ICE 服务器，没有配置 TURN 时为 nil
	turn   *turnServers
	logger *slog.Logger
	// 由 NewHub 设置。Calls 与 Hub 互相引用，wire 不允许构造时成环
	hub *Hub

	mu      sync.Mutex
	timers  map[int64]*time.Timer // 本实例发起的通话的振铃定时器
	stopped bool
}

// NewCalls 创建通话信令处理器。
func NewCalls(c *conf.Call, ws *conf.WebSocket, repo CallRepo, perm CallPermission, routes RouteRepo, fwd Forwarder, logger *slog.Logger) *Calls {
	ring := c.GetRingTimeout().AsDuration()
	return &Calls{
		ringTimeout: ring,
		ringingTTL:  ring + ringingTTLMargin,
		activeTTL:   ws.GetHeartbeatTimeout().AsDuration(),
		repo:        repo,
		perm:        perm,
		routes:      routes,
		fwd:         fwd,
		turn:        newTurnServers(c.GetTurn()),
		logger:      logger,
		timers:      make(map[int64]*time.Timer),
	}
}

// handle 处理一个通话请求，回复 CallAck 或 Error。ctx 带着帧的 Span，对 social 和其他 access 实例的调用接在它下面。
func (k *Calls) handle(ctx context.Context, c *Conn, rid uint64, req *v1.CallRequest) {
	if err := protovalidate.Validate(req); err != nil {
		c.push(errorFrame(rid, ErrInvalidFrame(err.Error())))
		return
	}
	ctx, cancel := context.WithTimeout(ctx, callOpTimeout)
	defer cancel()
	switch a := req.GetAction().(type) {
	case *v1.CallRequest_Invite:
		k.invite(ctx, c, rid, a.Invite)
	case *v1.CallRequest_Accept:
		k.accept(ctx, c, rid, a.Accept.GetCallId())
	case *v1.CallRequest_Reject:
		k.reject(ctx, c, rid, a.Reject.GetCallId())
	case *v1.CallRequest_Hangup:
		k.hangup(ctx, c, rid, a.Hangup.GetCallId())
	case *v1.CallRequest_Signal:
		k.signal(ctx, c, rid, a.Signal)
	}
}

func (k *Calls) invite(ctx context.Context, c *Conn, rid uint64, in *v1.CallInvite) {
	caller, callee := c.principal.UserID, in.GetCalleeId()
	if callee == caller {
		c.push(errorFrame(rid, ErrInvalidFrame("cannot call yourself")))
		return
	}
	if err := k.perm.CheckCall(ctx, caller, callee); err != nil {
		k.fail(c, rid, err)
		return
	}
	entries, err := k.listConns(ctx, callee)
	if err != nil {
		k.fail(c, rid, err)
		return
	}
	if len(entries) == 0 {
		k.fail(c, rid, ErrCallPeerOffline())
		return
	}
	call := &Call{
		CallKey:    CallKey{ID: rand.Int64N(math.MaxInt64) + 1, CallerID: caller, CalleeID: callee},
		CallerConn: c.id.Load(),
		CallerAddr: k.routes.Addr(),
		Media:      in.GetMedia(),
		State:      CallRinging,
		CreatedAt:  time.Now(),
	}
	res, err := k.repo.Create(ctx, call, k.ringingTTL)
	if err != nil {
		k.fail(c, rid, err)
		return
	}
	switch res {
	case CreateCallerBusy:
		k.fail(c, rid, ErrAlreadyInCall())
		return
	case CreateCalleeBusy:
		k.fail(c, rid, ErrCallBusy())
		return
	}
	c.call = callRef{key: call.CallKey, caller: true}
	// 先回复再推来电：被叫接听得再快，主叫也是先拿到 call_id，再收到 CallAccepted
	c.push(callAckWithIce(rid, call.ID, k.turn.issue(caller)))
	k.startRingTimer(call)
	countCallEvent("invited", v1.CallEndReason_CALL_END_REASON_UNSPECIFIED)
	k.logger.InfoContext(ctx, "call invited", slog.Int64("call_id", call.ID), slog.Int64("caller_id", caller),
		slog.Int64("callee_id", callee), slog.String("media", call.Media.String()))

	incoming := callEventFrame(&v1.CallEvent{Event: &v1.CallEvent_Incoming{Incoming: &v1.CallIncoming{
		CallId: call.ID, CallerId: caller, Media: call.Media,
	}}})
	if k.pushToConns(ctx, entries, incoming, 0) > 0 {
		return
	}
	// 路由还在，但被叫的连接其实都已断开（路由按心跳超时过期）：按对方离线结束
	ended, _, err := k.repo.End(ctx, call.CallKey, EndCond{By: PartyCallerConn, ID: call.CallerConn})
	if err != nil {
		k.logger.Warn("end call failed", slog.Int64("call_id", call.ID), slog.Any("error", err))
		return
	}
	if ended != nil {
		k.stopRingTimer(call.ID)
		c.call = callRef{}
		c.push(callEndedFrame(call.ID, v1.CallEndReason_CALL_END_REASON_PEER_OFFLINE))
		k.logEnded(ended, v1.CallEndReason_CALL_END_REASON_PEER_OFFLINE)
	}
}

func (k *Calls) accept(ctx context.Context, c *Conn, rid uint64, id int64) {
	call, err := k.repo.Get(ctx, id)
	if err != nil {
		k.fail(c, rid, err)
		return
	}
	if call == nil || call.CalleeID != c.principal.UserID {
		k.fail(c, rid, ErrCallNotFound())
		return
	}
	now := time.Now()
	ok, current, err := k.repo.Accept(ctx, call.CallKey, c.id.Load(), k.routes.Addr(), now, k.activeTTL)
	if err != nil {
		k.fail(c, rid, err)
		return
	}
	if !ok {
		k.fail(c, rid, stateError(current))
		return
	}
	call.State, call.CalleeConn, call.CalleeAddr, call.AnsweredAt = CallActive, c.id.Load(), k.routes.Addr(), now
	c.call = callRef{key: call.CallKey}
	// 先回复再通知主叫：主叫收到 CallAccepted 后才会发 offer，接听连接因此先拿到 CallAck
	c.push(callAckWithIce(rid, id, k.turn.issue(c.principal.UserID)))
	k.stopRingTimer(id)
	countCallEvent("accepted", v1.CallEndReason_CALL_END_REASON_UNSPECIFIED)
	k.logger.InfoContext(ctx, "call accepted", slog.Int64("call_id", id), slog.Duration("ringing", now.Sub(call.CreatedAt)))

	// 先让被叫的其他设备停止响铃：后面通知主叫失败时，通话按通话中结束，只会通知接听连接
	if entries, err := k.listConns(ctx, call.CalleeID); err == nil {
		k.pushToConns(ctx, entries, callEndedFrame(id, v1.CallEndReason_CALL_END_REASON_ANSWERED_ELSEWHERE), call.CalleeConn)
	}
	accepted := callEventFrame(&v1.CallEvent{Event: &v1.CallEvent_Accepted{Accepted: &v1.CallAccepted{CallId: id}}})
	if !k.deliver(ctx, call.CallerAddr, call.CallerConn, accepted) {
		k.endUnreachable(ctx, c, call.CallKey, PartyCalleeConn)
	}
}

func (k *Calls) reject(ctx context.Context, c *Conn, rid uint64, id int64) {
	call, err := k.repo.Get(ctx, id)
	if err != nil {
		k.fail(c, rid, err)
		return
	}
	if call == nil || call.CalleeID != c.principal.UserID {
		k.fail(c, rid, ErrCallNotFound())
		return
	}
	ended, current, err := k.repo.End(ctx, call.CallKey, EndCond{By: PartyCallee, ID: c.principal.UserID, State: CallRinging})
	if err != nil {
		k.fail(c, rid, err)
		return
	}
	if ended == nil {
		k.fail(c, rid, stateError(current))
		return
	}
	c.push(callAckFrame(rid, id))
	k.stopRingTimer(id)
	k.notifyEnded(ctx, ended, v1.CallEndReason_CALL_END_REASON_REJECTED, c.id.Load())
}

func (k *Calls) hangup(ctx context.Context, c *Conn, rid uint64, id int64) {
	if c.call.key.ID != id {
		k.fail(c, rid, ErrCallNotFound())
		return
	}
	cond := EndCond{By: PartyCallerConn, ID: c.id.Load()}
	if !c.call.caller {
		cond = EndCond{By: PartyCalleeConn, ID: c.id.Load(), State: CallActive}
	}
	ended, _, err := k.repo.End(ctx, c.call.key, cond)
	if err != nil {
		k.fail(c, rid, err)
		return
	}
	c.call = callRef{}
	if ended == nil {
		k.fail(c, rid, ErrCallNotFound())
		return
	}
	c.push(callAckFrame(rid, id))
	k.stopRingTimer(id)
	k.notifyEnded(ctx, ended, v1.CallEndReason_CALL_END_REASON_HANGUP, c.id.Load())
}

func (k *Calls) signal(ctx context.Context, c *Conn, rid uint64, s *v1.CallSignal) {
	id := s.GetCallId()
	if c.call.key.ID != id {
		k.fail(c, rid, ErrCallNotFound())
		return
	}
	call, err := k.repo.Get(ctx, id)
	if err != nil {
		k.fail(c, rid, err)
		return
	}
	connID := c.id.Load()
	if call == nil || call.State != CallActive {
		k.fail(c, rid, ErrCallNotFound())
		return
	}
	var addr string
	var peer int64
	var by CallParty
	switch connID {
	case call.CallerConn:
		addr, peer, by = call.CalleeAddr, call.CalleeConn, PartyCallerConn
	case call.CalleeConn:
		addr, peer, by = call.CallerAddr, call.CallerConn, PartyCalleeConn
	default:
		k.fail(c, rid, ErrCallNotFound())
		return
	}
	c.push(callAckFrame(rid, id))
	if !k.deliver(ctx, addr, peer, callEventFrame(&v1.CallEvent{Event: &v1.CallEvent_Signal{Signal: s}})) {
		k.endUnreachable(ctx, c, call.CallKey, by)
	}
}

// heartbeat 在连接心跳时续期它参与的通话。通话已结束或连接已不是通话方时，清掉连接上的记录。
func (k *Calls) heartbeat(ctx context.Context, c *Conn) {
	if c.call.key.ID == 0 {
		return
	}
	alive, err := k.repo.Touch(ctx, c.call.key, c.id.Load(), k.activeTTL)
	if err != nil {
		k.logger.Warn("touch call failed", slog.Int64("call_id", c.call.key.ID), slog.Any("error", err))
		return
	}
	if !alive {
		c.call = callRef{}
	}
}

// disconnect 在连接关闭时结束它参与的通话：主叫连接任意状态，接听连接在通话中。
func (k *Calls) disconnect(c *Conn) {
	if c.call.key.ID == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), callOpTimeout)
	defer cancel()
	cond := EndCond{By: PartyCallerConn, ID: c.id.Load()}
	if !c.call.caller {
		cond = EndCond{By: PartyCalleeConn, ID: c.id.Load()}
	}
	ended, _, err := k.repo.End(ctx, c.call.key, cond)
	if err != nil {
		// 结束失败时通话状态按 TTL 过期，期间双方的忙线标记保留
		k.logger.Warn("end call on disconnect failed", slog.Int64("call_id", c.call.key.ID), slog.Any("error", err))
		return
	}
	if ended != nil {
		k.stopRingTimer(ended.ID)
		k.notifyEnded(ctx, ended, v1.CallEndReason_CALL_END_REASON_DISCONNECTED, c.id.Load())
	}
}

// endUnreachable 在转发给对方连接失败时按断线结束通话。请求已经得到 CallAck，所以发起请求的连接也收到 CallEnded。
func (k *Calls) endUnreachable(ctx context.Context, c *Conn, key CallKey, by CallParty) {
	ended, _, err := k.repo.End(ctx, key, EndCond{By: by, ID: c.id.Load()})
	if err != nil {
		k.logger.Warn("end call failed", slog.Int64("call_id", key.ID), slog.Any("error", err))
		return
	}
	c.call = callRef{}
	if ended == nil {
		// 对方已经先结束了通话，它的 CallEnded 会经推送到达
		return
	}
	k.stopRingTimer(key.ID)
	k.notifyEnded(ctx, ended, v1.CallEndReason_CALL_END_REASON_DISCONNECTED, 0)
}

// notifyEnded 按结束前的状态通知通话的各方，skipConn 是已经得到回复的发起连接（0 表示不跳过）。
// 主叫连接总会收到；通话中只通知接听连接，振铃中通知被叫的全部连接。
func (k *Calls) notifyEnded(ctx context.Context, call *Call, reason v1.CallEndReason, skipConn int64) {
	k.logEnded(call, reason)
	frame := callEndedFrame(call.ID, reason)
	if call.CallerConn != skipConn {
		k.deliver(ctx, call.CallerAddr, call.CallerConn, frame)
	}
	if call.State == CallActive {
		if call.CalleeConn != skipConn {
			k.deliver(ctx, call.CalleeAddr, call.CalleeConn, frame)
		}
		return
	}
	if entries, err := k.listConns(ctx, call.CalleeID); err == nil {
		k.pushToConns(ctx, entries, frame, skipConn)
	}
}

func (k *Calls) logEnded(call *Call, reason v1.CallEndReason) {
	countCallEvent("ended", reason)
	now := time.Now()
	attrs := []any{slog.Int64("call_id", call.ID), slog.Int64("caller_id", call.CallerID),
		slog.Int64("callee_id", call.CalleeID), slog.String("reason", reason.String())}
	if call.AnsweredAt.IsZero() {
		attrs = append(attrs, slog.Duration("ringing", now.Sub(call.CreatedAt)))
	} else {
		attrs = append(attrs, slog.Duration("ringing", call.AnsweredAt.Sub(call.CreatedAt)),
			slog.Duration("talking", now.Sub(call.AnsweredAt)))
	}
	k.logger.Info("call ended", attrs...)
}

// listConns 返回用户的全部在线连接。部分路由无法解析时用能解析的部分。
func (k *Calls) listConns(ctx context.Context, userID int64) ([]route.Entry, error) {
	m, err := k.routes.List(ctx, []int64{userID})
	entries := m[userID]
	if err != nil {
		if len(entries) == 0 {
			return nil, ErrUpstreamUnavailable(err)
		}
		k.logger.Warn("some routes unreadable", slog.Int64("user_id", userID), slog.Any("error", err))
	}
	return entries, nil
}

// pushToConns 按实例分组推送，跳过 skipConn，返回实际放入发送队列的连接数。推送尽力而为，失败只记日志。
func (k *Calls) pushToConns(ctx context.Context, entries []route.Entry, frame *v1.ServerFrame, skipConn int64) int {
	byAddr := make(map[string][]int64)
	for _, e := range entries {
		if e.ConnID != skipConn {
			byAddr[e.Addr] = append(byAddr[e.Addr], e.ConnID)
		}
	}
	n := 0
	for addr, ids := range byAddr {
		got, err := k.push(ctx, addr, ids, frame)
		if err != nil {
			k.logger.Debug("push call frame failed", slog.String("addr", addr), slog.Any("error", err))
		}
		n += got
	}
	return n
}

// deliver 推给单个连接，返回是否已放入它的发送队列。
func (k *Calls) deliver(ctx context.Context, addr string, connID int64, frame *v1.ServerFrame) bool {
	n, err := k.push(ctx, addr, []int64{connID}, frame)
	if err != nil {
		k.logger.Debug("push call frame failed", slog.String("addr", addr), slog.Int64("conn_id", connID), slog.Any("error", err))
	}
	return n > 0
}

func (k *Calls) push(ctx context.Context, addr string, connIDs []int64, frame *v1.ServerFrame) (int, error) {
	if addr == k.routes.Addr() {
		return k.hub.Push(connIDs, frame), nil
	}
	return k.fwd.Push(ctx, addr, connIDs, frame)
}

// fail 回复错误。没有错误原因的错误是 Redis 等依赖的故障，按 503 返回。
func (k *Calls) fail(c *Conn, rid uint64, err error) {
	e := kerrors.FromError(err)
	if e.Reason == "" {
		err = ErrUpstreamUnavailable(err)
		e = kerrors.FromError(err)
	}
	if e.Code >= 500 {
		k.logger.Warn("call request failed", slog.Int64("conn_id", c.id.Load()), slog.Any("error", err))
	}
	c.push(errorFrame(rid, err))
}

// stateError 把“条件不满足”时的当前状态转成错误：已被接听返回 CALL_ALREADY_ANSWERED，其余为 CALL_NOT_FOUND。
func stateError(current CallState) error {
	if current == CallActive {
		return ErrCallAlreadyAnswered()
	}
	return ErrCallNotFound()
}

// startRingTimer 启动振铃定时器。定时器只在本实例，主叫所在实例崩溃时由客户端的振铃上限和 Redis TTL 兜底。
func (k *Calls) startRingTimer(call *Call) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.stopped {
		return
	}
	k.timers[call.ID] = time.AfterFunc(k.ringTimeout, func() { k.onRingTimeout(call) })
}

func (k *Calls) stopRingTimer(id int64) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if t, ok := k.timers[id]; ok {
		t.Stop()
		delete(k.timers, id)
	}
}

// onRingTimeout 在振铃超时时结束仍在振铃的通话。已被接听或已结束时脚本条件不满足，什么也不做。
func (k *Calls) onRingTimeout(call *Call) {
	k.mu.Lock()
	delete(k.timers, call.ID)
	k.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), callOpTimeout)
	defer cancel()
	ended, _, err := k.repo.End(ctx, call.CallKey, EndCond{By: PartyAny, State: CallRinging})
	if err != nil {
		k.logger.Warn("end call on ring timeout failed", slog.Int64("call_id", call.ID), slog.Any("error", err))
		return
	}
	if ended != nil {
		k.notifyEnded(ctx, ended, v1.CallEndReason_CALL_END_REASON_TIMEOUT, 0)
	}
}

// stop 取消全部振铃定时器，在 Hub 停机、连接全部关闭后调用。此时还在振铃的通话按 Redis TTL 过期。
func (k *Calls) stop() {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.stopped = true
	for id, t := range k.timers {
		t.Stop()
		delete(k.timers, id)
	}
}

func callAckFrame(rid uint64, id int64) *v1.ServerFrame {
	return callAckWithIce(rid, id, nil)
}

// callAckWithIce 用于邀请和接听：双方都在收到这个回复之后才创建 RTCPeerConnection（主叫要等 CallAccepted）
func callAckWithIce(rid uint64, id int64, ice []*v1.IceServer) *v1.ServerFrame {
	return &v1.ServerFrame{RequestId: rid, Body: &v1.ServerFrame_CallAck{CallAck: &v1.CallAck{CallId: id, IceServers: ice}}}
}

func callEventFrame(e *v1.CallEvent) *v1.ServerFrame {
	return &v1.ServerFrame{Body: &v1.ServerFrame_CallEvent{CallEvent: e}}
}

func callEndedFrame(id int64, reason v1.CallEndReason) *v1.ServerFrame {
	return callEventFrame(&v1.CallEvent{Event: &v1.CallEvent_Ended{Ended: &v1.CallEnded{CallId: id, Reason: reason}}})
}
