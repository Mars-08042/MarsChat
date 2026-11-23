package biz

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	v1 "github.com/Mars-08042/MarsChat/api/access/v1"
	"github.com/Mars-08042/MarsChat/app/access/internal/conf"
)

func TestTurnServersIssue(t *testing.T) {
	s := newTurnServers(&conf.Turn{
		Urls:   " turn:192.0.2.10:13478?transport=udp , turn:192.0.2.10:13478?transport=tcp,",
		Secret: "test-secret",
		Ttl:    durationpb.New(12 * time.Hour),
	})
	s.now = func() time.Time { return time.Unix(1700000000, 0) }

	got := s.issue(42)
	if len(got) != 1 {
		t.Fatalf("issue returned %d servers, want 1", len(got))
	}
	want := []string{"turn:192.0.2.10:13478?transport=udp", "turn:192.0.2.10:13478?transport=tcp"}
	if strings.Join(got[0].GetUrls(), " ") != strings.Join(want, " ") {
		t.Fatalf("urls = %q, want %q", got[0].GetUrls(), want)
	}
	// 过期时刻 = 1700000000 + 43200；密码是用 Python 的 hmac、hashlib.sha1、base64 独立算出的
	if got[0].GetUsername() != "1700043200:42" {
		t.Fatalf("username = %q, want 1700043200:42", got[0].GetUsername())
	}
	if got[0].GetCredential() != "YZH5FHh90sTb5539d4uKQsBuQeM=" {
		t.Fatalf("credential = %q", got[0].GetCredential())
	}
}

func TestTurnServersDisabledAndDefaultTTL(t *testing.T) {
	for _, urls := range []string{"", " , "} {
		if s := newTurnServers(&conf.Turn{Urls: urls, Secret: "x"}); s != nil {
			t.Fatalf("urls %q: want nil, got %+v", urls, s)
		}
	}
	// 没有配置时 issue 不下发任何服务器
	var none *turnServers
	if got := none.issue(42); got != nil {
		t.Fatalf("nil turnServers issued %v", got)
	}
	if s := newTurnServers(&conf.Turn{Urls: "turn:a:3478", Secret: "x"}); s.ttl != defaultTurnTTL {
		t.Fatalf("ttl = %v, want default %v", s.ttl, defaultTurnTTL)
	}
}

// 邀请与接听的 CallAck 带 ICE 服务器，用户名里是各自的用户 ID；信令和挂断的 CallAck 不带。
func TestCallAckIceServers(t *testing.T) {
	e := newEnvWithCall(t, nil, &conf.Call{
		RingTimeout: durationpb.New(time.Minute),
		Turn:        &conf.Turn{Urls: "turn:192.0.2.10:13478?transport=udp", Secret: "test-secret", Ttl: durationpb.New(time.Hour)},
	})
	a, b := e.dial(t), e.dial(t)
	a.login("ok-42-7")
	b.login("ok-43-8")

	before := time.Now()
	a.send(inviteFrame(10, 43))
	inviteAck := a.expectAckFrame(10)
	b.expectEvent()
	b.send(acceptFrame(20, inviteAck.GetCallId()))
	acceptAck := b.expectAckFrame(20)
	a.expectEvent()

	for _, c := range []struct {
		ack  *v1.CallAck
		user int64
	}{{inviteAck, 42}, {acceptAck, 43}} {
		servers := c.ack.GetIceServers()
		if len(servers) != 1 {
			t.Fatalf("user %d: ice servers = %v, want 1", c.user, servers)
		}
		expires, user, ok := strings.Cut(servers[0].GetUsername(), ":")
		if !ok || user != strconv.FormatInt(c.user, 10) {
			t.Fatalf("username = %q, want <expires>:%d", servers[0].GetUsername(), c.user)
		}
		at, _ := strconv.ParseInt(expires, 10, 64)
		if d := time.Unix(at, 0).Sub(before); d < time.Hour-time.Second || d > time.Hour+5*time.Second {
			t.Fatalf("expires in %v, want about 1h", d)
		}
		if servers[0].GetCredential() != turnCredential([]byte("test-secret"), servers[0].GetUsername()) {
			t.Fatalf("credential does not match username")
		}
	}

	id := inviteAck.GetCallId()
	a.send(signalFrame(30, sdpSignal(id, v1.SdpType_SDP_TYPE_OFFER, "v=0 offer")))
	if got := a.expectAckFrame(30).GetIceServers(); len(got) != 0 {
		t.Fatalf("signal ack carries ice servers: %v", got)
	}
	b.expectEvent()
	a.send(hangupFrame(31, id))
	if got := a.expectAckFrame(31).GetIceServers(); len(got) != 0 {
		t.Fatalf("hangup ack carries ice servers: %v", got)
	}
}

// 没有配置 TURN 时 CallAck 不带 ICE 服务器，与原来的行为一致。
func TestCallAckWithoutTurn(t *testing.T) {
	s := setupRinging(t)
	s.b1.send(acceptFrame(20, s.id))
	if got := s.b1.expectAckFrame(20).GetIceServers(); len(got) != 0 {
		t.Fatalf("ice servers = %v, want none", got)
	}
}
