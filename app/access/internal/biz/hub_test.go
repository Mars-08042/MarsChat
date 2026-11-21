package biz

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	kerrors "github.com/go-kratos/kratos/v3/errors"
	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	accountv1 "github.com/Mars-08042/MarsChat/api/account/v1"
	v1 "github.com/Mars-08042/MarsChat/api/access/v1"
	messagev1 "github.com/Mars-08042/MarsChat/api/message/v1"
	"github.com/Mars-08042/MarsChat/app/access/internal/conf"
	"github.com/Mars-08042/MarsChat/pkg/auth"
	"github.com/Mars-08042/MarsChat/pkg/route"
)

// ---- 假依赖 ----

type fakeAuth struct {
	mu        sync.Mutex
	revoked   map[int64]bool // 按登录会话 ID
	revokeErr error
}

// 令牌格式为 "ok-{user}-{session}"，只认测试里用到的几个。
var tokens = map[string]auth.Principal{
	"ok-42-7":  {UserID: 42, SessionID: 7},
	"ok-43-8":  {UserID: 43, SessionID: 8},
	"ok-43-9":  {UserID: 43, SessionID: 9},
	"ok-42-11": {UserID: 42, SessionID: 11},
	"ok-44-10": {UserID: 44, SessionID: 10},
}

func (a *fakeAuth) Authenticate(_ context.Context, token string) (auth.Principal, error) {
	p, ok := tokens[token]
	if !ok {
		return auth.Principal{}, auth.ErrUnauthorized("invalid token")
	}
	return p, nil
}

func (a *fakeAuth) IsRevoked(_ context.Context, p auth.Principal) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.revoked[p.SessionID], a.revokeErr
}

func (a *fakeAuth) revoke(sessionID int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.revoked[sessionID] = true
}

// 本实例在路由中的地址
const localAddr = "10.0.0.1:19100"

type fakeRoutes struct {
	mu        sync.Mutex
	m         map[int64]map[int64]int64 // user → conn → session
	registers int
	ttl       time.Duration
	// 模拟其他实例上的连接，List 时与本实例的连接一起返回
	remote map[int64][]route.Entry
}

func (r *fakeRoutes) Register(_ context.Context, userID, connID, sessionID int64, ttl time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.m[userID] == nil {
		r.m[userID] = map[int64]int64{}
	}
	r.m[userID][connID] = sessionID
	r.registers++
	r.ttl = ttl
	return nil
}

func (r *fakeRoutes) Unregister(_ context.Context, userID, connID int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.m[userID], connID)
	return nil
}

func (r *fakeRoutes) List(_ context.Context, userIDs []int64) (map[int64][]route.Entry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[int64][]route.Entry)
	for _, uid := range userIDs {
		for conn, session := range r.m[uid] {
			out[uid] = append(out[uid], route.Entry{ConnID: conn, SessionID: session, Addr: localAddr})
		}
		out[uid] = append(out[uid], r.remote[uid]...)
		if len(out[uid]) == 0 {
			delete(out, uid)
		}
	}
	return out, nil
}

func (r *fakeRoutes) Addr() string { return localAddr }

func (r *fakeRoutes) addRemote(userID int64, e route.Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.remote == nil {
		r.remote = map[int64][]route.Entry{}
	}
	r.remote[userID] = append(r.remote[userID], e)
}

func (r *fakeRoutes) has(userID, connID int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.m[userID][connID]
	return ok
}

func (r *fakeRoutes) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.registers
}

type sendCall struct {
	p      auth.Principal
	connID int64
	msg    *messagev1.OutgoingMessage
}

type fakeSender struct {
	mu    sync.Mutex
	calls []sendCall
	fn    func(m *messagev1.OutgoingMessage) (*messagev1.SendResult, error)
}

func (s *fakeSender) Send(_ context.Context, p auth.Principal, connID int64, m *messagev1.OutgoingMessage) (*messagev1.SendResult, error) {
	s.mu.Lock()
	s.calls = append(s.calls, sendCall{p, connID, m})
	fn := s.fn
	s.mu.Unlock()
	return fn(m)
}

// ---- 测试环境 ----

type env struct {
	hub    *Hub
	auth   *fakeAuth
	routes *fakeRoutes
	sender *fakeSender
	policy *PolicyHolder
	calls  *fakeCallRepo
	perm   *fakePerm
	fwd    *fakeForwarder
	url    string
}

func testConf() *conf.WebSocket {
	return &conf.WebSocket{
		AuthTimeout:       durationpb.New(2 * time.Second),
		HeartbeatInterval: durationpb.New(30 * time.Second),
		HeartbeatTimeout:  durationpb.New(90 * time.Second),
		SendQueue:         256,
		WriteTimeout:      durationpb.New(2 * time.Second),
		MaxFrameBytes:     65536,
	}
}

func newEnv(t *testing.T, mutate func(*conf.WebSocket)) *env {
	t.Helper()
	c := testConf()
	if mutate != nil {
		mutate(c)
	}
	e := &env{
		auth:   &fakeAuth{revoked: map[int64]bool{}},
		routes: &fakeRoutes{m: map[int64]map[int64]int64{}},
		sender: &fakeSender{fn: func(*messagev1.OutgoingMessage) (*messagev1.SendResult, error) {
			return &messagev1.SendResult{ConversationId: 100, Seq: 1}, nil
		}},
		policy: NewPolicyHolder(&conf.Policy{FramesPerSecond: 30}),
		calls:  newFakeCallRepo(),
		perm:   &fakePerm{},
		fwd:    &fakeForwarder{},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	calls := NewCalls(&conf.Call{RingTimeout: durationpb.New(time.Minute)}, c, e.calls, e.perm, e.routes, e.fwd, logger)
	e.hub = NewHub(c, e.policy, e.auth, e.routes, e.sender, calls, logger)
	upgrader := websocket.Upgrader{Subprotocols: Subprotocols}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		e.hub.Serve(ws)
	}))
	e.url = "ws" + strings.TrimPrefix(srv.URL, "http")
	t.Cleanup(srv.Close)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = e.hub.Stop(ctx)
	})
	return e
}

type client struct {
	t  *testing.T
	ws *websocket.Conn
}

func (e *env) dial(t *testing.T, subprotocols ...string) *client {
	t.Helper()
	d := websocket.Dialer{Subprotocols: subprotocols}
	ws, _, err := d.Dial(e.url, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	return &client{t: t, ws: ws}
}

func (c *client) send(f *v1.ClientFrame) {
	c.t.Helper()
	b, err := proto.Marshal(f)
	if err != nil {
		c.t.Fatal(err)
	}
	if err := c.ws.WriteMessage(websocket.BinaryMessage, b); err != nil {
		c.t.Fatal(err)
	}
}

func (c *client) recv() *v1.ServerFrame {
	c.t.Helper()
	_ = c.ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	typ, b, err := c.ws.ReadMessage()
	if err != nil {
		c.t.Fatalf("read: %v", err)
	}
	if typ != websocket.BinaryMessage {
		c.t.Fatalf("message type = %d, want binary", typ)
	}
	f := &v1.ServerFrame{}
	if err := proto.Unmarshal(b, f); err != nil {
		c.t.Fatal(err)
	}
	return f
}

// expectClose 读到关闭帧为止（丢弃之前的帧），检查关闭码。
func (c *client) expectClose(code int) {
	c.t.Helper()
	_ = c.ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		_, _, err := c.ws.ReadMessage()
		if err == nil {
			continue
		}
		var ce *websocket.CloseError
		if !errors.As(err, &ce) {
			c.t.Fatalf("read error = %v, want close %d", err, code)
		}
		if ce.Code != code {
			c.t.Fatalf("close code = %d (%s), want %d", ce.Code, ce.Text, code)
		}
		return
	}
}

func authFrame(rid uint64, token string) *v1.ClientFrame {
	return &v1.ClientFrame{RequestId: rid, Body: &v1.ClientFrame_Auth{Auth: &v1.Auth{AccessToken: token}}}
}

func pingFrame(rid uint64) *v1.ClientFrame {
	return &v1.ClientFrame{RequestId: rid, Body: &v1.ClientFrame_Ping{Ping: &v1.Ping{}}}
}

func sendFrame(rid uint64, clientMsgID string) *v1.ClientFrame {
	return &v1.ClientFrame{RequestId: rid, Body: &v1.ClientFrame_SendMessage{SendMessage: &messagev1.OutgoingMessage{
		PeerType: messagev1.ConversationType_CONVERSATION_TYPE_SINGLE, PeerId: 43, ClientMsgId: clientMsgID,
	}}}
}

// login 完成认证并返回连接 ID。
func (c *client) login(token string) *v1.AuthOk {
	c.t.Helper()
	c.send(authFrame(1, token))
	f := c.recv()
	ok := f.GetAuthOk()
	if ok == nil || f.GetRequestId() != 1 {
		c.t.Fatalf("want AuthOk with request_id 1, got %v", f)
	}
	return ok
}

func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func wantError(t *testing.T, f *v1.ServerFrame, rid uint64, code int32, reason string) {
	t.Helper()
	e := f.GetError()
	if e == nil || f.GetRequestId() != rid || e.GetCode() != code || e.GetReason() != reason {
		t.Fatalf("want Error{request_id %d, %d %s}, got %v", rid, code, reason, f)
	}
}

// ---- 用例 ----

func TestAuthAndHeartbeat(t *testing.T) {
	e := newEnv(t, nil)
	c := e.dial(t)
	ok := c.login("ok-42-7")
	if ok.GetUserId() != 42 || ok.GetSessionId() != 7 || ok.GetConnectionId() <= 0 || ok.GetHeartbeatIntervalSeconds() != 30 {
		t.Fatalf("AuthOk = %v", ok)
	}
	if !e.routes.has(42, ok.GetConnectionId()) || e.routes.ttl != 90*time.Second {
		t.Fatalf("route not registered with heartbeat timeout as ttl")
	}

	c.send(pingFrame(2))
	if f := c.recv(); f.GetPong() == nil || f.GetRequestId() != 2 {
		t.Fatalf("want Pong with request_id 2, got %v", f)
	}
	// 心跳续期路由
	if got := e.routes.count(); got != 2 {
		t.Fatalf("route registers = %d, want 2", got)
	}

	_ = c.ws.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	eventually(t, func() bool { return !e.routes.has(42, ok.GetConnectionId()) })
}

func TestJSONSubprotocol(t *testing.T) {
	e := newEnv(t, nil)
	c := e.dial(t, SubprotocolJSON)
	if c.ws.Subprotocol() != SubprotocolJSON {
		t.Fatalf("subprotocol = %q", c.ws.Subprotocol())
	}
	if err := c.ws.WriteMessage(websocket.TextMessage, []byte(`{"requestId":"5","auth":{"accessToken":"ok-42-7"}}`)); err != nil {
		t.Fatal(err)
	}
	_ = c.ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	typ, b, err := c.ws.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if typ != websocket.TextMessage || !strings.Contains(string(b), `"requestId":"5"`) || !strings.Contains(string(b), `"userId":"42"`) {
		t.Fatalf("got type %d: %s", typ, b)
	}
}

func TestFirstFrameMustBeAuth(t *testing.T) {
	e := newEnv(t, nil)
	c := e.dial(t)
	c.send(pingFrame(3))
	wantError(t, c.recv(), 3, 400, "INVALID_FRAME")
	c.expectClose(CloseUnauthenticated)
}

func TestInvalidToken(t *testing.T) {
	e := newEnv(t, nil)
	c := e.dial(t)
	c.send(authFrame(1, "bad"))
	wantError(t, c.recv(), 1, 401, "UNAUTHORIZED")
	c.expectClose(CloseUnauthenticated)
	if e.routes.count() != 0 {
		t.Fatal("route registered for unauthenticated connection")
	}
}

func TestAuthTimeout(t *testing.T) {
	e := newEnv(t, func(c *conf.WebSocket) { c.AuthTimeout = durationpb.New(100 * time.Millisecond) })
	c := e.dial(t)
	c.expectClose(CloseUnauthenticated)
}

// 认证后超过心跳超时没有任何帧，服务端断开并删除路由。
func TestHeartbeatTimeout(t *testing.T) {
	e := newEnv(t, func(c *conf.WebSocket) { c.HeartbeatTimeout = durationpb.New(200 * time.Millisecond) })
	c := e.dial(t)
	ok := c.login("ok-42-7")
	_ = c.ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, _, err := c.ws.ReadMessage(); err == nil {
		t.Fatal("want connection closed")
	}
	eventually(t, func() bool { return !e.routes.has(42, ok.GetConnectionId()) })
}

func TestSendMessage(t *testing.T) {
	e := newEnv(t, nil)
	c := e.dial(t)
	ok := c.login("ok-42-7")

	c.send(sendFrame(10, "m1"))
	f := c.recv()
	if f.GetSendAck().GetSeq() != 1 || f.GetRequestId() != 10 {
		t.Fatalf("want SendAck for request 10, got %v", f)
	}
	e.sender.mu.Lock()
	call := e.sender.calls[0]
	e.sender.mu.Unlock()
	if call.p.UserID != 42 || call.p.SessionID != 7 || call.connID != ok.GetConnectionId() || call.msg.GetClientMsgId() != "m1" {
		t.Fatalf("send call = %+v", call)
	}

	// message 的业务错误原样返回，包括 metadata
	e.sender.mu.Lock()
	e.sender.fn = func(*messagev1.OutgoingMessage) (*messagev1.SendResult, error) {
		return nil, kerrors.New(429, "RATE_LIMITED", "发送过于频繁").WithMetadata(map[string]string{"retry_after_seconds": "3"})
	}
	e.sender.mu.Unlock()
	c.send(sendFrame(11, "m2"))
	f = c.recv()
	wantError(t, f, 11, 429, "RATE_LIMITED")
	if f.GetError().GetMetadata()["retry_after_seconds"] != "3" {
		t.Fatalf("metadata lost: %v", f)
	}

	// 非 Kratos 错误不暴露细节
	e.sender.mu.Lock()
	e.sender.fn = func(*messagev1.OutgoingMessage) (*messagev1.SendResult, error) {
		return nil, errors.New("dial tcp 10.0.0.1:19030: connection refused")
	}
	e.sender.mu.Unlock()
	c.send(sendFrame(12, "m3"))
	f = c.recv()
	wantError(t, f, 12, 500, "INTERNAL")
	if strings.Contains(f.GetError().GetMessage(), "10.0.0.1") {
		t.Fatalf("internal detail leaked: %v", f)
	}
}

// 发消息在读协程中同步处理：后发的帧在前一条消息确认之后才得到响应。
func TestSendIsOrderedWithinConnection(t *testing.T) {
	e := newEnv(t, nil)
	e.sender.fn = func(m *messagev1.OutgoingMessage) (*messagev1.SendResult, error) {
		if m.GetClientMsgId() == "slow" {
			time.Sleep(200 * time.Millisecond)
		}
		return &messagev1.SendResult{Seq: 1}, nil
	}
	c := e.dial(t)
	c.login("ok-42-7")
	c.send(sendFrame(20, "slow"))
	c.send(pingFrame(21))
	if f := c.recv(); f.GetRequestId() != 20 || f.GetSendAck() == nil {
		t.Fatalf("first response = %v, want SendAck 20", f)
	}
	if f := c.recv(); f.GetRequestId() != 21 || f.GetPong() == nil {
		t.Fatalf("second response = %v, want Pong 21", f)
	}
}

func TestInvalidFramesAfterAuth(t *testing.T) {
	e := newEnv(t, nil)
	c := e.dial(t)
	c.login("ok-42-7")

	if err := c.ws.WriteMessage(websocket.BinaryMessage, []byte{0xff, 0xff, 0xff}); err != nil {
		t.Fatal(err)
	}
	wantError(t, c.recv(), 0, 400, "INVALID_FRAME")
	c.send(authFrame(30, "ok-42-7"))
	wantError(t, c.recv(), 30, 400, "INVALID_FRAME")
	c.send(&v1.ClientFrame{RequestId: 31})
	wantError(t, c.recv(), 31, 400, "INVALID_FRAME")
	// 连接仍然可用
	c.send(pingFrame(32))
	if f := c.recv(); f.GetPong() == nil {
		t.Fatalf("want Pong, got %v", f)
	}
}

func TestPushAndKick(t *testing.T) {
	e := newEnv(t, nil)
	c := e.dial(t)
	ok := c.login("ok-42-7")
	id := ok.GetConnectionId()

	frame := &v1.ServerFrame{Body: &v1.ServerFrame_Message{Message: &messagev1.Message{ConversationId: 100, Seq: 5}}}
	if n := e.hub.Push([]int64{id, 999}, frame); n != 1 {
		t.Fatalf("Push delivered = %d, want 1", n)
	}
	if f := c.recv(); f.GetMessage().GetSeq() != 5 || f.GetRequestId() != 0 {
		t.Fatalf("want pushed message, got %v", f)
	}

	if n := e.hub.Kick([]int64{id}, accountv1.RevokeReason_REVOKE_REASON_BANNED); n != 1 {
		t.Fatalf("Kick = %d, want 1", n)
	}
	if f := c.recv(); f.GetKicked().GetReason() != accountv1.RevokeReason_REVOKE_REASON_BANNED {
		t.Fatalf("want Kicked BANNED, got %v", f)
	}
	c.expectClose(CloseKicked)
	eventually(t, func() bool { return !e.routes.has(42, id) })
	if n := e.hub.Push([]int64{id}, frame); n != 0 {
		t.Fatalf("Push after close = %d, want 0", n)
	}
}

// 吊销事件丢失时，心跳复查吊销名单兜底断开。
func TestRevokedOnHeartbeat(t *testing.T) {
	e := newEnv(t, nil)
	c := e.dial(t)
	c.login("ok-42-7")
	e.auth.revoke(7)
	c.send(pingFrame(2))
	if f := c.recv(); f.GetKicked() == nil || f.GetKicked().GetReason() != accountv1.RevokeReason_REVOKE_REASON_UNSPECIFIED {
		t.Fatalf("want Kicked, got %v", f)
	}
	c.expectClose(CloseKicked)
}

// Redis 故障无法复查吊销时放行，与 HTTP 接口一致。
func TestRevocationCheckFailureKeepsConnection(t *testing.T) {
	e := newEnv(t, nil)
	e.auth.revokeErr = errors.New("redis down")
	c := e.dial(t)
	c.login("ok-42-7")
	c.send(pingFrame(2))
	if f := c.recv(); f.GetPong() == nil {
		t.Fatalf("want Pong, got %v", f)
	}
}

func TestFrameRateLimit(t *testing.T) {
	e := newEnv(t, nil)
	c := e.dial(t)
	c.login("ok-42-7")
	// 热更新后立即生效；认证帧已经占用了当前窗口的 1 帧
	e.policy.Store(&conf.Policy{FramesPerSecond: 3})
	for i := range 5 {
		c.send(pingFrame(uint64(10 + i)))
	}
	c.expectClose(CloseRateLimited)
}

func TestOversizedFrame(t *testing.T) {
	e := newEnv(t, func(c *conf.WebSocket) { c.MaxFrameBytes = 1024 })
	c := e.dial(t)
	c.login("ok-42-7")
	if err := c.ws.WriteMessage(websocket.BinaryMessage, make([]byte, 2048)); err != nil {
		t.Fatal(err)
	}
	c.expectClose(websocket.CloseMessageTooBig)
}

// 发送队列满时以 4003 断开。这里不启动写协程，队列不会被消费。
func TestSlowConsumer(t *testing.T) {
	e := newEnv(t, func(c *conf.WebSocket) { c.SendQueue = 1 })
	serverSide := make(chan *websocket.Conn, 1)
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		serverSide <- ws
	}))
	t.Cleanup(srv.Close)
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Close() })

	conn := newConn(e.hub, <-serverSide)
	frame := &v1.ServerFrame{Body: &v1.ServerFrame_Pong{Pong: &v1.Pong{}}}
	if !conn.push(frame) {
		t.Fatal("first push should fit in the queue")
	}
	if conn.push(frame) {
		t.Fatal("second push should overflow")
	}
	if conn.push(frame) {
		t.Fatal("push after close should be dropped")
	}
	(&client{t: t, ws: ws}).expectClose(CloseSlowConsumer)
}

func TestStopClosesConnections(t *testing.T) {
	e := newEnv(t, nil)
	c := e.dial(t)
	ok := c.login("ok-42-7")
	pending := e.dial(t) // 尚未认证的连接也要关闭

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := e.hub.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	c.expectClose(websocket.CloseGoingAway)
	pending.expectClose(websocket.CloseGoingAway)
	if e.routes.has(42, ok.GetConnectionId()) {
		t.Fatal("route not removed after Stop returned")
	}
	// 停机后的新连接立即关闭
	e.dial(t).expectClose(websocket.CloseGoingAway)
}

// closeEvent 是客户端观察到的连接结束：关闭码（异常断开为 0）和时间。
type closeEvent struct {
	code int
	at   time.Time
}

// watchClose 在后台读到连接结束为止，把结果发到返回的通道。
func watchClose(ws *websocket.Conn) <-chan closeEvent {
	ch := make(chan closeEvent, 1)
	go func() {
		_ = ws.SetReadDeadline(time.Now().Add(10 * time.Second))
		for {
			if _, _, err := ws.ReadMessage(); err != nil {
				ev := closeEvent{at: time.Now()}
				var ce *websocket.CloseError
				if errors.As(err, &ce) {
					ev.code = ce.Code
				}
				ch <- ev
				return
			}
		}
	}()
	return ch
}

// 停机时连接分批关闭，分布在 drain_window 内，而不是同时断开。
func TestStopDrainsInBatches(t *testing.T) {
	const n = 20
	window := time.Second // 10 批，每批 2 个，最后一批在约 900 毫秒时关闭
	e := newEnv(t, func(c *conf.WebSocket) { c.DrainWindow = durationpb.New(window) })
	watches := make([]<-chan closeEvent, n)
	for i := range watches {
		watches[i] = watchClose(e.dial(t).ws)
	}
	eventually(t, func() bool {
		e.hub.mu.Lock()
		defer e.hub.mu.Unlock()
		return len(e.hub.all) == n
	})

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := e.hub.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	stopped := time.Since(start)

	var first, last time.Duration
	for i, w := range watches {
		ev := <-w
		if ev.code != websocket.CloseGoingAway {
			t.Fatalf("connection %d close code = %d, want 1001", i, ev.code)
		}
		d := ev.at.Sub(start)
		if i == 0 || d < first {
			first = d
		}
		last = max(last, d)
	}
	if first > 300*time.Millisecond {
		t.Errorf("first close after %v, want the first batch immediately", first)
	}
	if last < 700*time.Millisecond {
		t.Errorf("last close after %v, want spread over the %v window", last, window)
	}
	if stopped < last-50*time.Millisecond {
		t.Errorf("Stop returned after %v, before the last connection closed at %v", stopped, last)
	}
	select {
	case <-e.hub.Drained():
	default:
		t.Error("Drained not closed after Stop returned")
	}
}

// 停机超时到期时不再分批等待，剩余连接立即断开，Stop 返回超时错误。
func TestStopDeadlineCutsDrainShort(t *testing.T) {
	const n = 10
	e := newEnv(t, func(c *conf.WebSocket) { c.DrainWindow = durationpb.New(10 * time.Second) })
	watches := make([]<-chan closeEvent, n)
	for i := range watches {
		watches[i] = watchClose(e.dial(t).ws)
	}
	eventually(t, func() bool {
		e.hub.mu.Lock()
		defer e.hub.mu.Unlock()
		return len(e.hub.all) == n
	})

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := e.hub.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stop = %v, want DeadlineExceeded", err)
	}
	for i, w := range watches {
		select {
		case ev := <-w:
			if d := ev.at.Sub(start); d > 2*time.Second {
				t.Errorf("connection %d ended after %v, want right after the deadline", i, d)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("connection %d still open after Stop returned", i)
		}
	}
}
