package biz

import (
	"testing"

	"github.com/gorilla/websocket"
	"go.opentelemetry.io/otel/attribute"

	v1 "github.com/Mars-08042/MarsChat/api/access/v1"
	"github.com/Mars-08042/MarsChat/pkg/telemetry/telemetrytest"
)

func frames(t *testing.T, typ string) int64 {
	return telemetrytest.Sum(t, "marschat_ws_frames", attribute.String("type", typ))
}

func closes(t *testing.T, code string) int64 {
	return telemetrytest.Sum(t, "marschat_ws_closes", attribute.String("code", code))
}

// 认证后在线连接数加一、断开后减一；上行帧按类型计数；关闭原因区分服务端关闭码与对端关闭。
// 指标在包内测试之间累计（测试不并行），比较差值。
func TestConnectionMetrics(t *testing.T) {
	telemetrytest.Reader(t)
	e := newEnv(t, nil)
	conns := func() int64 { return telemetrytest.Sum(t, "marschat_ws_connections") }
	baseConns := conns()
	baseAuth, basePing, baseSend := frames(t, "auth"), frames(t, "ping"), frames(t, "send_message")
	baseNormal, baseUnauth := closes(t, closePeerNormal), closes(t, serverCloseLabel(CloseUnauthenticated))

	c := e.dial(t)
	c.login("ok-42-7")
	if got := conns() - baseConns; got != 1 {
		t.Fatalf("connections += %d after login, want 1", got)
	}
	c.send(pingFrame(2))
	c.recv()
	c.send(sendFrame(3, "m1"))
	c.recv()
	if a, p, s := frames(t, "auth")-baseAuth, frames(t, "ping")-basePing, frames(t, "send_message")-baseSend; a != 1 || p != 1 || s != 1 {
		t.Errorf("frames += auth %d, ping %d, send_message %d, want 1 each", a, p, s)
	}

	_ = c.ws.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	eventually(t, func() bool { return conns() == baseConns })
	eventually(t, func() bool { return closes(t, closePeerNormal)-baseNormal == 1 })

	// 未认证就发业务帧：服务端以 4001 关闭，不计入在线连接
	u := e.dial(t)
	u.send(&v1.ClientFrame{RequestId: 1, Body: &v1.ClientFrame_Ping{Ping: &v1.Ping{}}})
	u.expectClose(CloseUnauthenticated)
	eventually(t, func() bool { return closes(t, serverCloseLabel(CloseUnauthenticated))-baseUnauth == 1 })
	if got := conns(); got != baseConns {
		t.Errorf("connections = %d after unauthenticated close, want %d", got, baseConns)
	}
}

func TestPeerCloseLabel(t *testing.T) {
	for code, want := range map[int]string{1000: closePeerNormal, 1001: closePeerNormal, 1006: closePeerOther, 4999: closePeerOther} {
		if got := peerCloseLabel(code); got != want {
			t.Errorf("peerCloseLabel(%d) = %s, want %s", code, got, want)
		}
	}
}
