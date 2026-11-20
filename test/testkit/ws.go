package testkit

import (
	"errors"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"

	accessv1 "github.com/Mars-08042/MarsChat/api/access/v1"
	messagev1 "github.com/Mars-08042/MarsChat/api/message/v1"
)

// WS 是 protobuf 子协议的 WebSocket 测试客户端。Conn 公开，便于测试直接写原始帧或关闭帧。
type WS struct {
	t    testing.TB
	Conn *websocket.Conn
}

// Dial 连接 url（直连 access 或经网关），失败时终止测试。连接在测试结束时关闭。
func Dial(t testing.TB, url string, subprotocols ...string) *WS {
	t.Helper()
	c, err := DialE(t, url, subprotocols...)
	if err != nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	return c
}

// DialE 与 Dial 相同，但把连接错误返回给调用方，用于故障实验中的重连循环。
func DialE(t testing.TB, url string, subprotocols ...string) (*WS, error) {
	d := websocket.Dialer{Subprotocols: subprotocols, HandshakeTimeout: 5 * time.Second}
	ws, _, err := d.Dial(url, nil)
	if err != nil {
		return nil, err
	}
	return Wrap(t, ws), nil
}

// Wrap 包装一个已经建立的连接（例如自定义了 TLS 配置的 WSS 连接），连接在测试结束时关闭。
func Wrap(t testing.TB, ws *websocket.Conn) *WS {
	t.Cleanup(func() { _ = ws.Close() })
	return &WS{t: t, Conn: ws}
}

// Send 发送一帧。
func (c *WS) Send(f *accessv1.ClientFrame) {
	c.t.Helper()
	b, err := proto.Marshal(f)
	if err != nil {
		c.t.Fatal(err)
	}
	if err := c.Conn.WriteMessage(websocket.BinaryMessage, b); err != nil {
		c.t.Fatal(err)
	}
}

// Recv 读取一帧，5 秒内读不到时终止测试。
func (c *WS) Recv() *accessv1.ServerFrame {
	c.t.Helper()
	f, err := c.RecvE(5 * time.Second)
	if err != nil {
		c.t.Fatalf("read: %v", err)
	}
	return f
}

// RecvE 读取一帧并返回读错误（包括对端关闭），用于需要处理断线的场景。
func (c *WS) RecvE(timeout time.Duration) (*accessv1.ServerFrame, error) {
	_ = c.Conn.SetReadDeadline(time.Now().Add(timeout))
	_, b, err := c.Conn.ReadMessage()
	if err != nil {
		return nil, err
	}
	f := &accessv1.ServerFrame{}
	if err := proto.Unmarshal(b, f); err != nil {
		return nil, err
	}
	return f, nil
}

// Call 发送一帧并等待同一 request_id 的响应，期间收到的推送被丢弃。
func (c *WS) Call(f *accessv1.ClientFrame) *accessv1.ServerFrame {
	c.t.Helper()
	c.Send(f)
	for {
		if r := c.Recv(); r.GetRequestId() == f.GetRequestId() {
			return r
		}
	}
}

// ExpectClose 读到指定关闭码为止，期间的其他帧被丢弃。
func (c *WS) ExpectClose(code int) {
	c.t.Helper()
	_ = c.Conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		_, _, err := c.Conn.ReadMessage()
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

// Auth 发送首帧认证并返回 AuthOk。
func (c *WS) Auth(token string) *accessv1.AuthOk {
	c.t.Helper()
	r := c.Call(&accessv1.ClientFrame{RequestId: 1, Body: &accessv1.ClientFrame_Auth{Auth: &accessv1.Auth{AccessToken: token}}})
	if r.GetAuthOk() == nil {
		c.t.Fatalf("auth: %v", r)
	}
	return r.GetAuthOk()
}

// SendText 发送一条文本消息（内容为 "ws " + clientID）并返回确认帧或错误帧。
func (c *WS) SendText(rid uint64, peerType messagev1.ConversationType, peer int64, clientID string) *accessv1.ServerFrame {
	c.t.Helper()
	return c.Call(&accessv1.ClientFrame{RequestId: rid, Body: &accessv1.ClientFrame_SendMessage{SendMessage: &messagev1.OutgoingMessage{
		PeerType: peerType, PeerId: peer, ClientMsgId: clientID, Content: TextContent("ws " + clientID),
	}}})
}

// Ping 发送心跳并返回 Pong。
func (c *WS) Ping(rid uint64) *accessv1.ServerFrame {
	c.t.Helper()
	return c.Call(&accessv1.ClientFrame{RequestId: rid, Body: &accessv1.ClientFrame_Ping{Ping: &accessv1.Ping{}}})
}

// WaitFrame 读到满足 match 的帧为止，期间的其他帧被丢弃。
func (c *WS) WaitFrame(what string, match func(*accessv1.ServerFrame) bool) *accessv1.ServerFrame {
	c.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if f := c.Recv(); match(f) {
			return f
		}
	}
	c.t.Fatalf("no %s within 15s", what)
	return nil
}

// ExpectNoMessage 发送 Ping 并读到 Pong 为止，期间不能收到消息推送。同一连接的帧按入队顺序发送，
// 只要另一个接收者已经收到同一次推送，这里就不会漏判。
func (c *WS) ExpectNoMessage(rid uint64) {
	c.t.Helper()
	c.Send(&accessv1.ClientFrame{RequestId: rid, Body: &accessv1.ClientFrame_Ping{Ping: &accessv1.Ping{}}})
	for {
		f := c.Recv()
		if m := f.GetMessage(); m != nil {
			c.t.Fatalf("unexpected message push: conversation %d seq %d", m.GetConversationId(), m.GetSeq())
		}
		if f.GetPong() != nil && f.GetRequestId() == rid {
			return
		}
	}
}

// IsMessage 匹配指定会话、序号的消息推送。
func IsMessage(conv, seq int64) func(*accessv1.ServerFrame) bool {
	return func(f *accessv1.ServerFrame) bool {
		m := f.GetMessage()
		return m != nil && m.GetConversationId() == conv && m.GetSeq() == seq
	}
}

// IsRead 匹配指定会话、用户、序号的已读推送。
func IsRead(conv, user, seq int64) func(*accessv1.ServerFrame) bool {
	return func(f *accessv1.ServerFrame) bool {
		r := f.GetRead()
		return r != nil && r.GetConversationId() == conv && r.GetUserId() == user && r.GetReadSeq() == seq
	}
}

// TextContent 构造文本消息内容。
func TextContent(s string) *messagev1.MessageContent {
	return &messagev1.MessageContent{Body: &messagev1.MessageContent_Text{Text: &messagev1.TextContent{Text: s}}}
}
