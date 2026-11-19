package server

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/go-kratos/kratos/v3/transport/http"
	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	v1 "github.com/Mars-08042/MarsChat/api/access/v1"
	messagev1 "github.com/Mars-08042/MarsChat/api/message/v1"
	"github.com/Mars-08042/MarsChat/app/access/internal/biz"
	"github.com/Mars-08042/MarsChat/app/access/internal/conf"
	"github.com/Mars-08042/MarsChat/pkg/auth"
)

type allowAll struct{}

func (allowAll) Authenticate(context.Context, string) (auth.Principal, error) {
	return auth.Principal{UserID: 42, SessionID: 7}, nil
}
func (allowAll) IsRevoked(context.Context, auth.Principal) (bool, error) { return false, nil }

type noRoutes struct{}

func (noRoutes) Register(context.Context, int64, int64, int64, time.Duration) error { return nil }
func (noRoutes) Unregister(context.Context, int64, int64) error                   { return nil }

type noSender struct{}

func (noSender) Send(context.Context, auth.Principal, int64, *messagev1.OutgoingMessage) (*messagev1.SendResult, error) {
	return &messagev1.SendResult{}, nil
}

// Kratos HTTP 服务给每个请求的 context 加了服务端超时。WebSocket 挂在 Kratos 的路由上，
// 连接的生命周期必须与它无关：超时过后连接仍然可用。
func TestWebSocketOutlivesHTTPTimeout(t *testing.T) {
	hub := biz.NewHub(&conf.WebSocket{
		AuthTimeout:       durationpb.New(2 * time.Second),
		HeartbeatInterval: durationpb.New(30 * time.Second),
		HeartbeatTimeout:  durationpb.New(90 * time.Second),
		SendQueue:         16,
		WriteTimeout:      durationpb.New(2 * time.Second),
		MaxFrameBytes:     65536,
	}, biz.NewPolicyHolder(&conf.Policy{FramesPerSecond: 30}), allowAll{}, noRoutes{}, noSender{},
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	const timeout = 50 * time.Millisecond
	srv := http.NewServer(http.Address("127.0.0.1:0"), http.Timeout(timeout))
	srv.Handle("/ws", NewWebSocketHandler(hub))
	u, err := srv.Endpoint()
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Start(context.Background()) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hub.Stop(ctx)
		_ = srv.Stop(ctx)
	})

	ws, _, err := websocket.DefaultDialer.Dial("ws://"+u.Host+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	roundTrip := func(f *v1.ClientFrame) *v1.ServerFrame {
		t.Helper()
		b, _ := proto.Marshal(f)
		if err := ws.WriteMessage(websocket.BinaryMessage, b); err != nil {
			t.Fatal(err)
		}
		_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
		_, data, err := ws.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		out := &v1.ServerFrame{}
		if err := proto.Unmarshal(data, out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	if f := roundTrip(&v1.ClientFrame{RequestId: 1, Body: &v1.ClientFrame_Auth{Auth: &v1.Auth{AccessToken: "x"}}}); f.GetAuthOk() == nil {
		t.Fatalf("want AuthOk, got %v", f)
	}
	time.Sleep(4 * timeout)
	if f := roundTrip(&v1.ClientFrame{RequestId: 2, Body: &v1.ClientFrame_Ping{Ping: &v1.Ping{}}}); f.GetPong() == nil {
		t.Fatalf("want Pong after the HTTP timeout, got %v", f)
	}
}
