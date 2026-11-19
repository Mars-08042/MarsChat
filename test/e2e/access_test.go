//go:build e2e

package e2e

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"

	accessv1 "github.com/Mars-08042/MarsChat/api/access/v1"
	accountv1 "github.com/Mars-08042/MarsChat/api/account/v1"
	messagev1 "github.com/Mars-08042/MarsChat/api/message/v1"
)

// access 经 Consul 调用 message，message 再调用 social、account，所以这四个服务都要启动。
const (
	accessWS   = "ws://127.0.0.1:18000/ws"
	accessGRPC = "127.0.0.1:19100"
)

type wsClient struct {
	t  *testing.T
	ws *websocket.Conn
}

func dialWS(t *testing.T, subprotocols ...string) *wsClient {
	t.Helper()
	d := websocket.Dialer{Subprotocols: subprotocols, HandshakeTimeout: 5 * time.Second}
	ws, _, err := d.Dial(accessWS, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", accessWS, err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	return &wsClient{t: t, ws: ws}
}

func (c *wsClient) send(f *accessv1.ClientFrame) {
	c.t.Helper()
	b, err := proto.Marshal(f)
	if err != nil {
		c.t.Fatal(err)
	}
	if err := c.ws.WriteMessage(websocket.BinaryMessage, b); err != nil {
		c.t.Fatal(err)
	}
}

func (c *wsClient) recv() *accessv1.ServerFrame {
	c.t.Helper()
	_ = c.ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, b, err := c.ws.ReadMessage()
	if err != nil {
		c.t.Fatalf("read: %v", err)
	}
	f := &accessv1.ServerFrame{}
	if err := proto.Unmarshal(b, f); err != nil {
		c.t.Fatal(err)
	}
	return f
}

// call 发送一帧并等待同一 request_id 的响应，期间收到的推送被丢弃。
func (c *wsClient) call(f *accessv1.ClientFrame) *accessv1.ServerFrame {
	c.t.Helper()
	c.send(f)
	for {
		if r := c.recv(); r.GetRequestId() == f.GetRequestId() {
			return r
		}
	}
}

func (c *wsClient) expectClose(code int) {
	c.t.Helper()
	_ = c.ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		_, _, err := c.ws.ReadMessage()
		if err == nil {
			continue
		}
		var ce *websocket.CloseError
		if !errors.As(err, &ce) || ce.Code != code {
			c.t.Fatalf("read error = %v, want close %d", err, code)
		}
		return
	}
}

func (c *wsClient) auth(token string) *accessv1.AuthOk {
	c.t.Helper()
	r := c.call(&accessv1.ClientFrame{RequestId: 1, Body: &accessv1.ClientFrame_Auth{Auth: &accessv1.Auth{AccessToken: token}}})
	if r.GetAuthOk() == nil {
		c.t.Fatalf("auth: %v", r)
	}
	return r.GetAuthOk()
}

func (c *wsClient) sendText(rid uint64, peerType messagev1.ConversationType, peer int64, clientID string) *accessv1.ServerFrame {
	c.t.Helper()
	return c.call(&accessv1.ClientFrame{RequestId: rid, Body: &accessv1.ClientFrame_SendMessage{SendMessage: &messagev1.OutgoingMessage{
		PeerType: peerType, PeerId: peer, ClientMsgId: clientID, Content: textContent("ws " + clientID),
	}}})
}

func (c *wsClient) ping(rid uint64) *accessv1.ServerFrame {
	c.t.Helper()
	return c.call(&accessv1.ClientFrame{RequestId: rid, Body: &accessv1.ClientFrame_Ping{Ping: &accessv1.Ping{}}})
}

func wantFrameError(t *testing.T, f *accessv1.ServerFrame, code int32, reason string) {
	t.Helper()
	if e := f.GetError(); e == nil || e.GetCode() != code || e.GetReason() != reason {
		t.Fatalf("want Error %d %s, got %v", code, reason, f)
	}
}

func routeFields(t *testing.T, userID int64) map[string]string {
	t.Helper()
	m, err := newRedisClient(t).HGetAll(context.Background(), "access:route:"+strconv.FormatInt(userID, 10)).Result()
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestAccessSendOverWebSocket(t *testing.T) {
	resetRateLimits(t)
	a, b, stranger := newUser(t), newUser(t), newUser(t)
	makeFriends(t, a, b)

	c := dialWS(t)
	ok := c.auth(a.token)
	if ok.GetUserId() != a.id || ok.GetHeartbeatIntervalSeconds() != 30 {
		t.Fatalf("AuthOk = %v", ok)
	}
	connField := strconv.FormatInt(ok.GetConnectionId(), 10)
	route := routeFields(t, a.id)[connField]
	if !strings.HasPrefix(route, strconv.FormatInt(ok.GetSessionId(), 10)+"|") || !strings.HasSuffix(route, "|"+accessGRPC) {
		t.Fatalf("route value = %q", route)
	}

	single := messagev1.ConversationType_CONVERSATION_TYPE_SINGLE
	r1 := c.sendText(10, single, b.id, "w1")
	if r1.GetSendAck().GetSeq() != 1 {
		t.Fatalf("first send = %v", r1)
	}
	// 重发返回首次的结果
	if dup := c.sendText(11, single, b.id, "w1"); dup.GetSendAck().GetSeq() != 1 ||
		dup.GetSendAck().GetConversationId() != r1.GetSendAck().GetConversationId() {
		t.Fatalf("duplicate = %v", dup)
	}
	if r := c.sendText(12, single, b.id, "w2"); r.GetSendAck().GetSeq() != 2 {
		t.Fatalf("second send = %v", r)
	}
	// message 的错误原样出现在 Error 帧中
	wantFrameError(t, c.sendText(13, single, stranger.id, "w3"), 403, "NOT_FRIENDS")
	wantFrameError(t, c.sendText(14, single, b.id, "bad id!"), 400, "VALIDATOR")

	if r := c.ping(15); r.GetPong() == nil {
		t.Fatalf("ping = %v", r)
	}
	got := waitConversation(t, b.token, r1.GetSendAck().GetConversationId())
	if got.GetMaxSeq() != 2 || got.GetLastMessage().GetContent().GetText().GetText() != "ws w2" {
		t.Fatalf("b's conversation = %v", got)
	}

	_ = c.ws.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	deadline := time.Now().Add(3 * time.Second)
	for routeFields(t, a.id)[connField] != "" {
		if time.Now().After(deadline) {
			t.Fatal("route not removed after close")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestAccessJSONSubprotocol(t *testing.T) {
	resetRateLimits(t)
	a := newUser(t)
	c := dialWS(t, "marschat.v1.json")
	if err := c.ws.WriteMessage(websocket.TextMessage, []byte(`{"requestId":"7","auth":{"accessToken":"`+a.token+`"}}`)); err != nil {
		t.Fatal(err)
	}
	_ = c.ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	typ, b, err := c.ws.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if typ != websocket.TextMessage || !strings.Contains(string(b), `"authOk"`) || !strings.Contains(string(b), `"userId":"`+id(a.id)+`"`) {
		t.Fatalf("got type %d: %s", typ, b)
	}
}

func TestAccessRejectsInvalidToken(t *testing.T) {
	c := dialWS(t)
	r := c.call(&accessv1.ClientFrame{RequestId: 1, Body: &accessv1.ClientFrame_Auth{Auth: &accessv1.Auth{AccessToken: "not-a-token"}}})
	wantFrameError(t, r, 401, "UNAUTHORIZED")
	c.expectClose(4001)
}

// 退出登录后连接被踢：delivery 运行时由事件触发，否则由下一次心跳复查吊销名单触发。
// 两种情况都是先收到 Kicked 帧，再以 4002 关闭。
func TestAccessKickedAfterLogout(t *testing.T) {
	resetRateLimits(t)
	a := newUser(t)
	c := dialWS(t)
	c.auth(a.token)
	mustCall(t, http.MethodPost, "/v1/auth/logout", a.token, &accountv1.LogoutRequest{}, nil)
	// 事件可能已经先把连接踢掉，写失败可以忽略
	if b, err := proto.Marshal(&accessv1.ClientFrame{RequestId: 2, Body: &accessv1.ClientFrame_Ping{Ping: &accessv1.Ping{}}}); err == nil {
		_ = c.ws.WriteMessage(websocket.BinaryMessage, b)
	}
	for {
		if f := c.recv(); f.GetKicked() != nil {
			if f.GetKicked().GetReason() != accountv1.RevokeReason_REVOKE_REASON_UNSPECIFIED &&
				f.GetKicked().GetReason() != accountv1.RevokeReason_REVOKE_REASON_LOGOUT {
				t.Fatalf("kicked reason = %v", f.GetKicked().GetReason())
			}
			break
		}
	}
	c.expectClose(4002)
}
