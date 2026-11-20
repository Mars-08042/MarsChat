//go:build e2e

package e2e

import (
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
	"github.com/Mars-08042/MarsChat/test/testkit"
)

// access 经 Consul 调用 message，message 再调用 social、account，所以这四个服务都要启动。
const (
	accessWS   = "ws://127.0.0.1:18000/ws"
	accessGRPC = "127.0.0.1:19100"
)

type wsClient = testkit.WS

func dialWS(t *testing.T, subprotocols ...string) *wsClient {
	t.Helper()
	return testkit.Dial(t, accessWS, subprotocols...)
}

func wantFrameError(t *testing.T, f *accessv1.ServerFrame, code int32, reason string) {
	t.Helper()
	if e := f.GetError(); e == nil || e.GetCode() != code || e.GetReason() != reason {
		t.Fatalf("want Error %d %s, got %v", code, reason, f)
	}
}

func routeFields(t *testing.T, userID int64) map[string]string {
	t.Helper()
	return testkit.RouteFields(t, newRedisClient(t), userID)
}

func TestAccessSendOverWebSocket(t *testing.T) {
	resetRateLimits(t)
	a, b, stranger := newUser(t), newUser(t), newUser(t)
	makeFriends(t, a, b)

	c := dialWS(t)
	ok := c.Auth(a.token)
	if ok.GetUserId() != a.id || ok.GetHeartbeatIntervalSeconds() != 30 {
		t.Fatalf("AuthOk = %v", ok)
	}
	connField := strconv.FormatInt(ok.GetConnectionId(), 10)
	route := routeFields(t, a.id)[connField]
	if !strings.HasPrefix(route, strconv.FormatInt(ok.GetSessionId(), 10)+"|") || !strings.HasSuffix(route, "|"+accessGRPC) {
		t.Fatalf("route value = %q", route)
	}

	single := messagev1.ConversationType_CONVERSATION_TYPE_SINGLE
	r1 := c.SendText(10, single, b.id, "w1")
	if r1.GetSendAck().GetSeq() != 1 {
		t.Fatalf("first send = %v", r1)
	}
	// 重发返回首次的结果
	if dup := c.SendText(11, single, b.id, "w1"); dup.GetSendAck().GetSeq() != 1 ||
		dup.GetSendAck().GetConversationId() != r1.GetSendAck().GetConversationId() {
		t.Fatalf("duplicate = %v", dup)
	}
	if r := c.SendText(12, single, b.id, "w2"); r.GetSendAck().GetSeq() != 2 {
		t.Fatalf("second send = %v", r)
	}
	// message 的错误原样出现在 Error 帧中
	wantFrameError(t, c.SendText(13, single, stranger.id, "w3"), 403, "NOT_FRIENDS")
	wantFrameError(t, c.SendText(14, single, b.id, "bad id!"), 400, "VALIDATOR")

	if r := c.Ping(15); r.GetPong() == nil {
		t.Fatalf("ping = %v", r)
	}
	got := waitConversation(t, b.token, r1.GetSendAck().GetConversationId())
	if got.GetMaxSeq() != 2 || got.GetLastMessage().GetContent().GetText().GetText() != "ws w2" {
		t.Fatalf("b's conversation = %v", got)
	}

	_ = c.Conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
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
	if err := c.Conn.WriteMessage(websocket.TextMessage, []byte(`{"requestId":"7","auth":{"accessToken":"`+a.token+`"}}`)); err != nil {
		t.Fatal(err)
	}
	_ = c.Conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	typ, b, err := c.Conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if typ != websocket.TextMessage || !strings.Contains(string(b), `"authOk"`) || !strings.Contains(string(b), `"userId":"`+id(a.id)+`"`) {
		t.Fatalf("got type %d: %s", typ, b)
	}
}

func TestAccessRejectsInvalidToken(t *testing.T) {
	c := dialWS(t)
	r := c.Call(&accessv1.ClientFrame{RequestId: 1, Body: &accessv1.ClientFrame_Auth{Auth: &accessv1.Auth{AccessToken: "not-a-token"}}})
	wantFrameError(t, r, 401, "UNAUTHORIZED")
	c.ExpectClose(4001)
}

// 退出登录后连接被踢：delivery 运行时由事件触发，否则由下一次心跳复查吊销名单触发。
// 两种情况都是先收到 Kicked 帧，再以 4002 关闭。
func TestAccessKickedAfterLogout(t *testing.T) {
	resetRateLimits(t)
	a := newUser(t)
	c := dialWS(t)
	c.Auth(a.token)
	mustCall(t, http.MethodPost, "/v1/auth/logout", a.token, &accountv1.LogoutRequest{}, nil)
	// 事件可能已经先把连接踢掉，写失败可以忽略
	if b, err := proto.Marshal(&accessv1.ClientFrame{RequestId: 2, Body: &accessv1.ClientFrame_Ping{Ping: &accessv1.Ping{}}}); err == nil {
		_ = c.Conn.WriteMessage(websocket.BinaryMessage, b)
	}
	for {
		if f := c.Recv(); f.GetKicked() != nil {
			if f.GetKicked().GetReason() != accountv1.RevokeReason_REVOKE_REASON_UNSPECIFIED &&
				f.GetKicked().GetReason() != accountv1.RevokeReason_REVOKE_REASON_LOGOUT {
				t.Fatalf("kicked reason = %v", f.GetKicked().GetReason())
			}
			break
		}
	}
	c.ExpectClose(4002)
}
