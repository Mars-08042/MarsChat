//go:build cluster

package cluster

import (
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	accessv1 "github.com/Mars-08042/MarsChat/api/access/v1"
	messagev1 "github.com/Mars-08042/MarsChat/api/message/v1"
	"github.com/Mars-08042/MarsChat/test/testkit"
)

var (
	errNotConnected = errors.New("not connected")
	errDisconnected = errors.New("disconnected before the response")
	errTimeout      = errors.New("no response in time")
)

// closeEvent 是一次断线：关闭码（连接异常中断记为 1006）和时间。
type closeEvent struct {
	code int
	at   time.Time
}

// outage 是一次从断线到重新认证成功的时间段，pulled 为重连后补拉到的消息数。
type outage struct {
	down, up time.Time
	pulled   int
}

// session 是按 data-flow.md 第 7 节的客户端约定自动重连的 WebSocket 客户端，供故障实验使用：
//   - 断线（收到关闭帧或连接异常中断）后随机等待 0～2 秒，经网关重连并认证；
//   - 重连后对关注的会话从本地连续的最大 seq 补拉（GetDifference），补拉与推送按 seq 去重；
//   - 每 20 秒发一次 Ping，保持连接和在线路由。
//
// 推送和补拉分别记录，用于检查不丢不重以及推送是否恢复。
type session struct {
	t    *testing.T
	name string
	u    user
	conv atomic.Int64 // 关注的会话，0 表示还不知道，不补拉

	mu       sync.Mutex
	ws       *websocket.Conn
	addr     string // 第一次连接所在的 access 实例
	nextRID  uint64
	pending  map[uint64]chan *accessv1.ServerFrame
	pushes   map[int64]int       // seq → 收到推送的次数
	pushedAt map[int64]time.Time // seq → 第一次收到推送的时间
	order    []int64             // 推送到达的顺序
	pulled   map[int64]bool      // 经补拉拿到的 seq
	closes   []closeEvent
	outages  []outage

	stop chan struct{}
	done chan struct{}
}

// startSession 经网关连接并认证，want 不为 nil 时反复连接直到落在满足它的 access 实例上，然后在后台维持连接。
func startSession(t *testing.T, name string, u user, want func(addr string) bool) *session {
	t.Helper()
	s := &session{
		t: t, name: name, u: u,
		nextRID:  1, // request_id 1 用于认证
		pending:  map[uint64]chan *accessv1.ServerFrame{},
		pushes:   map[int64]int{},
		pushedAt: map[int64]time.Time{},
		pulled:   map[int64]bool{},
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	rdb := testkit.NewRedis(t)
	for i := 0; ; i++ {
		if i == 20 {
			t.Fatalf("%s: no connection landed on the wanted instance after 20 attempts", name)
		}
		ws, connID, err := s.dial()
		if err != nil {
			t.Fatalf("%s: connect: %v", name, err)
		}
		addr := routeAddr(t, rdb, u.id, connID)
		if want == nil || want(addr) {
			s.ws, s.addr = ws, addr
			break
		}
		_ = ws.Close()
	}
	go s.loop()
	go s.heartbeat()
	t.Cleanup(s.close)
	return s
}

// routeAddr 从在线路由读出连接所在的 access 实例。
func routeAddr(t *testing.T, rdb *redis.Client, userID, connID int64) string {
	t.Helper()
	field := strconv.FormatInt(connID, 10)
	deadline := time.Now().Add(3 * time.Second)
	for {
		if v := testkit.RouteFields(t, rdb, userID)[field]; v != "" {
			return v[strings.LastIndex(v, "|")+1:]
		}
		if time.Now().After(deadline) {
			t.Fatalf("route of connection %d not registered", connID)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// dial 建立连接并认证，返回连接和连接 ID。
func (s *session) dial() (*websocket.Conn, int64, error) {
	d := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	ws, _, err := d.Dial(gatewayWS, nil)
	if err != nil {
		return nil, 0, err
	}
	b, _ := proto.Marshal(&accessv1.ClientFrame{RequestId: 1, Body: &accessv1.ClientFrame_Auth{Auth: &accessv1.Auth{AccessToken: s.u.token}}})
	_ = ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := ws.WriteMessage(websocket.BinaryMessage, b); err != nil {
		_ = ws.Close()
		return nil, 0, err
	}
	_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, data, err := ws.ReadMessage()
	if err != nil {
		_ = ws.Close()
		return nil, 0, err
	}
	f := &accessv1.ServerFrame{}
	if err := proto.Unmarshal(data, f); err != nil || f.GetAuthOk() == nil {
		_ = ws.Close()
		return nil, 0, fmt.Errorf("auth failed: %v %v", f, err)
	}
	return ws, f.GetAuthOk().GetConnectionId(), nil
}

func (s *session) loop() {
	defer close(s.done)
	ws := s.ws
	for {
		code := s.read(ws)
		down := time.Now()
		s.mu.Lock()
		s.ws = nil
		for rid, ch := range s.pending {
			close(ch)
			delete(s.pending, rid)
		}
		s.closes = append(s.closes, closeEvent{code: code, at: down})
		s.mu.Unlock()
		if !s.sleep(time.Duration(rand.Int64N(int64(2 * time.Second)))) {
			return
		}
		for {
			var err error
			ws, _, err = s.dial()
			if err == nil {
				break
			}
			if !s.sleep(300 * time.Millisecond) {
				return
			}
		}
		s.mu.Lock()
		s.ws = ws
		s.mu.Unlock()
		pulled := s.pull()
		s.mu.Lock()
		s.outages = append(s.outages, outage{down: down, up: time.Now(), pulled: pulled})
		s.mu.Unlock()
	}
}

// read 读到连接结束为止，分发响应、记录推送，返回关闭码。
func (s *session) read(ws *websocket.Conn) int {
	for {
		_ = ws.SetReadDeadline(time.Now().Add(2 * time.Minute))
		_, data, err := ws.ReadMessage()
		if err != nil {
			var ce *websocket.CloseError
			if errors.As(err, &ce) {
				return ce.Code
			}
			return websocket.CloseAbnormalClosure
		}
		f := &accessv1.ServerFrame{}
		if proto.Unmarshal(data, f) != nil {
			continue
		}
		s.mu.Lock()
		if rid := f.GetRequestId(); rid != 0 {
			if ch, ok := s.pending[rid]; ok {
				ch <- f
				delete(s.pending, rid)
			}
		} else if m := f.GetMessage(); m != nil {
			// 测试用户都是新注册的，只有一个会话：还不知道会话 ID 时取第一条推送的
			s.conv.CompareAndSwap(0, m.GetConversationId())
			if m.GetConversationId() != s.conv.Load() {
				s.mu.Unlock()
				continue
			}
			seq := m.GetSeq()
			if s.pushes[seq] == 0 {
				s.pushedAt[seq] = time.Now()
				s.order = append(s.order, seq)
			}
			s.pushes[seq]++
		}
		s.mu.Unlock()
	}
}

func (s *session) heartbeat() {
	tick := time.NewTicker(20 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-tick.C:
			s.mu.Lock()
			if s.ws != nil {
				b, _ := proto.Marshal(&accessv1.ClientFrame{Body: &accessv1.ClientFrame_Ping{Ping: &accessv1.Ping{}}})
				_ = s.ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
				_ = s.ws.WriteMessage(websocket.BinaryMessage, b)
			}
			s.mu.Unlock()
		}
	}
}

func (s *session) sleep(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-s.stop:
		return false
	case <-t.C:
		return true
	}
}

func (s *session) close() {
	select {
	case <-s.stop:
		return
	default:
	}
	close(s.stop)
	s.mu.Lock()
	if s.ws != nil {
		_ = s.ws.Close()
	}
	s.mu.Unlock()
	<-s.done
}

// send 发送一条单聊文本消息并等待响应（SendAck 或 Error）。
func (s *session) send(peer int64, clientMsgID string) (*accessv1.ServerFrame, error) {
	ch := make(chan *accessv1.ServerFrame, 1)
	s.mu.Lock()
	if s.ws == nil {
		s.mu.Unlock()
		return nil, errNotConnected
	}
	s.nextRID++
	rid := s.nextRID
	s.pending[rid] = ch
	b, _ := proto.Marshal(&accessv1.ClientFrame{RequestId: rid, Body: &accessv1.ClientFrame_SendMessage{SendMessage: &messagev1.OutgoingMessage{
		PeerType: messagev1.ConversationType_CONVERSATION_TYPE_SINGLE, PeerId: peer, ClientMsgId: clientMsgID,
		Content: testkit.TextContent(clientMsgID),
	}}})
	_ = s.ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
	err := s.ws.WriteMessage(websocket.BinaryMessage, b)
	if err != nil {
		delete(s.pending, rid)
	}
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	select {
	case f, ok := <-ch:
		if !ok {
			return nil, errDisconnected
		}
		return f, nil
	case <-time.After(5 * time.Second):
		s.mu.Lock()
		delete(s.pending, rid)
		s.mu.Unlock()
		return nil, errTimeout
	}
}

// sent 是一条发送成功的消息。
type sent struct {
	seq      int64
	ackedAt  time.Time
	attempts int
}

// sendFailure 是一次没有得到确认的发送尝试。
type sendFailure struct {
	at     time.Time
	reason string
}

// sendUntilAcked 用同一个 client_msg_id 重发，直到得到确认或超过 limit。失败的尝试追加到 failures。
func (s *session) sendUntilAcked(peer int64, clientMsgID string, limit time.Duration, failures *[]sendFailure) (sent, error) {
	deadline := time.Now().Add(limit)
	for attempt := 1; ; attempt++ {
		f, err := s.send(peer, clientMsgID)
		if ack := f.GetSendAck(); err == nil && ack != nil {
			if c := ack.GetConversationId(); s.conv.Load() == 0 {
				s.conv.Store(c)
			}
			return sent{seq: ack.GetSeq(), ackedAt: time.Now(), attempts: attempt}, nil
		}
		reason := ""
		switch {
		case err != nil:
			reason = err.Error()
		case f.GetError() != nil:
			reason = fmt.Sprintf("%d %s", f.GetError().GetCode(), f.GetError().GetReason())
		default:
			reason = fmt.Sprintf("unexpected frame %v", f)
		}
		*failures = append(*failures, sendFailure{at: time.Now(), reason: reason})
		if time.Now().After(deadline) {
			return sent{}, fmt.Errorf("%s not acknowledged after %v (%d attempts), last: %s", clientMsgID, limit, attempt, reason)
		}
		// 重发也计入 message 的发送限流（每人每 10 秒 30 条），间隔不能太短
		time.Sleep(500 * time.Millisecond)
	}
}

// pull 从本地连续的最大 seq 补拉关注的会话，返回补拉到的新消息数。
func (s *session) pull() int {
	conv := s.conv.Load()
	if conv == 0 {
		return 0
	}
	from := s.contiguous()
	n := 0
	for range 50 {
		var resp messagev1.GetDifferenceResponse
		path := fmt.Sprintf("/v1/conversations/%d/difference?from_seq=%d&limit=200", conv, from)
		if err := getJSON(path, s.u.token, &resp); err != nil {
			return n
		}
		s.mu.Lock()
		for _, m := range resp.GetMessages() {
			if s.pushes[m.GetSeq()] == 0 && !s.pulled[m.GetSeq()] {
				n++
			}
			s.pulled[m.GetSeq()] = true
		}
		s.mu.Unlock()
		if resp.GetResult() != messagev1.DifferenceResult_DIFFERENCE_RESULT_SLICE {
			return n
		}
		from = resp.GetNextFromSeq()
	}
	return n
}

// contiguous 返回 k，使 1..k 都已收到（推送或补拉）。
func (s *session) contiguous() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := int64(0)
	for s.pushes[k+1] > 0 || s.pulled[k+1] {
		k++
	}
	return k
}

// connected 报告当前是否已连接。
func (s *session) connected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ws != nil
}

// snapshot 返回断线和重连记录的副本。
func (s *session) snapshot() ([]closeEvent, []outage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.closes), slices.Clone(s.outages)
}

// pushedBy 返回 seq 第一次收到推送的时间，没有收到时 ok 为 false。
func (s *session) pushedBy(seq int64) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	at, ok := s.pushedAt[seq]
	return at, ok
}

// waitPushed 等待收到 seq 的推送。
func (s *session) waitPushed(t *testing.T, seq int64, timeout time.Duration) time.Time {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if at, ok := s.pushedBy(seq); ok {
			return at
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: seq %d not pushed within %v", s.name, seq, timeout)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitConnected 等待重新连上。
func (s *session) waitConnected(t *testing.T, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !s.connected() {
		if time.Now().After(deadline) {
			t.Fatalf("%s: not reconnected within %v", s.name, timeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// stats 返回推送覆盖的条数、重复推送的条数、只经补拉拿到的条数。
func (s *session) stats(n int64) (pushed, duplicated, pulledOnly int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for seq := int64(1); seq <= n; seq++ {
		switch c := s.pushes[seq]; {
		case c > 0:
			pushed++
			if c > 1 {
				duplicated++
			}
		case s.pulled[seq]:
			pulledOnly++
		}
	}
	return pushed, duplicated, pulledOnly
}

// getJSON 经网关发 GET 请求并解码，失败时返回错误而不是终止测试，可以在后台协程中使用。
func getJSON(path, token string, resp proto.Message) error {
	r, err := http.NewRequest(http.MethodGet, gatewayHTTP+path, nil)
	if err != nil {
		return err
	}
	r.Header.Set("Authorization", "Bearer "+token)
	c := http.Client{Timeout: 5 * time.Second}
	res, err := c.Do(r)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: status %d: %s", path, res.StatusCode, data)
	}
	return protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal(data, resp)
}

// verifyConversation 检查不丢不重：服务端历史恰好是 seq 1..n，client_msg_id 不重复；
// 每个接收方补拉之后收到的 seq 恰好是 1..n。
func verifyConversation(t *testing.T, conv int64, n int64, token string, receivers ...*session) {
	t.Helper()
	seen := map[int64]bool{}
	ids := map[string]bool{}
	before := int64(0)
	for {
		var page messagev1.ListMessagesResponse
		path := fmt.Sprintf("/v1/conversations/%d/messages?limit=100", conv)
		if before > 0 {
			path += "&before_seq=" + strconv.FormatInt(before, 10)
		}
		if err := getJSON(path, token, &page); err != nil {
			t.Fatal(err)
		}
		for _, m := range page.GetMessages() {
			if seen[m.GetSeq()] || ids[m.GetClientMsgId()] {
				t.Fatalf("duplicate in history: seq %d client_msg_id %s", m.GetSeq(), m.GetClientMsgId())
			}
			seen[m.GetSeq()], ids[m.GetClientMsgId()] = true, true
		}
		if before = page.GetNextBeforeSeq(); before == 0 {
			break
		}
	}
	if int64(len(seen)) != n {
		t.Fatalf("history has %d messages, want %d", len(seen), n)
	}
	for seq := int64(1); seq <= n; seq++ {
		if !seen[seq] {
			t.Fatalf("history misses seq %d", seq)
		}
	}
	for _, r := range receivers {
		r.pull()
		if got := r.contiguous(); got != n {
			t.Fatalf("%s: received 1..%d after the final pull, want 1..%d", r.name, got, n)
		}
		r.mu.Lock()
		for seq := range r.pushes {
			if seq > n {
				r.mu.Unlock()
				t.Fatalf("%s: pushed seq %d beyond %d", r.name, seq, n)
			}
		}
		r.mu.Unlock()
	}
}
