//go:build cluster

package cluster

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	messagev1 "github.com/Mars-08042/MarsChat/api/message/v1"
	"github.com/Mars-08042/MarsChat/test/testkit"
)

// 网关只转发对外接口：未认证请求由服务返回 Kratos 错误，未定义的路径和健康检查由网关返回 404。
func TestGatewayRoutes(t *testing.T) {
	for _, path := range []string{"/v1/users/me", "/v1/friends", "/v1/conversations"} {
		status, e := testkit.Call(t, gatewayHTTP, http.MethodGet, path, "", nil, nil)
		if status != http.StatusUnauthorized || e.Reason != "UNAUTHORIZED" {
			t.Errorf("GET %s: status %d, %+v; want 401 UNAUTHORIZED from the service", path, status, e)
		}
	}
	for _, path := range []string{"/healthz", "/readyz", "/v1/unknown"} {
		res, err := http.Get(gatewayHTTP + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s: status %d, want 404 from the gateway", path, res.StatusCode)
		}
	}
}

// HTTPS 与 WSS 使用 make certs 生成的自签证书。
func TestGatewayTLS(t *testing.T) {
	pem, err := os.ReadFile("../../deploy/apisix/certs/tls.crt")
	if err != nil {
		t.Fatalf("read gateway certificate (run make certs): %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		t.Fatal("invalid certificate")
	}
	cfg := &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg}, Timeout: 5 * time.Second}
	res, err := client.Get(gatewayHTTPS + "/v1/users/me")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("HTTPS status %d, want 401", res.StatusCode)
	}

	resetRateLimits(t)
	u := newUser(t)
	d := websocket.Dialer{TLSClientConfig: cfg, HandshakeTimeout: 5 * time.Second}
	ws, _, err := d.Dial("wss://localhost:19443/ws", nil)
	if err != nil {
		t.Fatalf("dial wss: %v", err)
	}
	if ok := testkit.Wrap(t, ws).Auth(u.token); ok.GetUserId() != u.id {
		t.Fatalf("AuthOk over WSS = %v", ok)
	}
}

// account 只信任网关转发的 X-Forwarded-For：客户端伪造的地址被忽略，限流计数落在网关看到的来源地址上。
func TestGatewayClientIP(t *testing.T) {
	rdb := testkit.NewRedis(t)
	resetRateLimits(t)
	const spoofed = "198.51.100.7"
	_ = rdb.Del(ctx(t), "account:rl:email_code_ip:"+spoofed).Err()

	req, err := http.NewRequest(http.MethodPost, gatewayHTTP+"/v1/auth/email-codes",
		strings.NewReader(`{"email":"`+testkit.RandomEmail(t)+`","purpose":"CODE_PURPOSE_REGISTER"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", spoofed)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("send email code: status %d", res.StatusCode)
	}
	if n, _ := rdb.Exists(ctx(t), "account:rl:email_code_ip:"+gatewayClientIP).Result(); n != 1 {
		t.Errorf("rate limit key for %s not found", gatewayClientIP)
	}
	if n, _ := rdb.Exists(ctx(t), "account:rl:email_code_ip:"+spoofed).Result(); n != 0 {
		t.Errorf("spoofed X-Forwarded-For %s was trusted", spoofed)
	}
	resetRateLimits(t)
}

// 两个用户分别连在不同的 access 实例上，消息经 delivery 按在线路由跨实例推送；
// 发送者在另一个实例上的第二条连接也收到推送（跨实例多端同步）。
func TestCrossNodePush(t *testing.T) {
	resetRateLimits(t)
	a, b := newUser(t), newUser(t)
	makeFriends(t, a, b)

	a1 := connect(t, a)
	b1 := connectWhere(t, b, "an instance other than "+a1.addr, func(addr string) bool { return addr != a1.addr })
	a2 := connectWhere(t, a, b1.addr, func(addr string) bool { return addr == b1.addr })
	t.Logf("a1 on %s, b1 and a2 on %s", a1.addr, b1.addr)

	single := messagev1.ConversationType_CONVERSATION_TYPE_SINGLE
	ack := a1.SendText(1, single, b.id, "x1").GetSendAck()
	if ack.GetSeq() != 1 {
		t.Fatalf("send ack = %v", ack)
	}
	start := time.Now()
	b1.WaitFrame("cross-node push to peer", testkit.IsMessage(ack.GetConversationId(), 1))
	t.Logf("cross-node push latency %v", time.Since(start))
	a2.WaitFrame("cross-node push to sender's other connection", testkit.IsMessage(ack.GetConversationId(), 1))
	a1.ExpectNoMessage(2)

	// 反方向：从 b1 所在实例发往 a1 所在实例
	back := b1.SendText(3, single, a.id, "x2").GetSendAck()
	a1.WaitFrame("reverse cross-node push", testkit.IsMessage(back.GetConversationId(), back.GetSeq()))
}

// 发送请求轮流落到两个 message 实例上，只有一个持有 Outbox 租约。不持有租约的实例经 Redis 频道唤醒持有者，
// 推送不应等到持有者下一轮轮询（0.5 秒）。没有跨实例唤醒时，约一半的推送要多等 95～500 毫秒。
func TestCrossNodePushLatency(t *testing.T) {
	resetRateLimits(t)
	a, b := newUser(t), newUser(t)
	makeFriends(t, a, b)
	a1 := connect(t, a)
	b1 := connectWhere(t, b, "an instance other than "+a1.addr, func(addr string) bool { return addr != a1.addr })

	const n, slowLimit = 10, 150 * time.Millisecond
	single := messagev1.ConversationType_CONVERSATION_TYPE_SINGLE
	latencies := make([]time.Duration, 0, n)
	slow := 0
	for i := 1; i <= n; i++ {
		start := time.Now()
		ack := a1.SendText(uint64(i), single, b.id, fmt.Sprintf("lat-%d", i)).GetSendAck()
		if ack == nil {
			t.Fatalf("send %d not acknowledged", i)
		}
		b1.WaitFrame("cross-node push", testkit.IsMessage(ack.GetConversationId(), ack.GetSeq()))
		d := time.Since(start)
		latencies = append(latencies, d)
		if d > slowLimit {
			slow++
		}
	}
	t.Logf("send-to-push latencies: %v", latencies)
	if slow > 1 {
		t.Errorf("%d of %d pushes took longer than %v, the Outbox wake across instances may not work", slow, n, slowLimit)
	}
}

// 入口限流：短时间内的大量请求有一部分被网关以 429 拒绝。放在最后，结束后等计数窗口过去。
func TestGatewayRateLimit(t *testing.T) {
	var limited, ok atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 300; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := http.Get(gatewayHTTP + "/v1/users/me")
			if err != nil {
				return
			}
			_ = res.Body.Close()
			switch res.StatusCode {
			case http.StatusTooManyRequests:
				limited.Add(1)
			case http.StatusUnauthorized:
				ok.Add(1)
			}
		}()
	}
	wg.Wait()
	t.Logf("300 concurrent requests: %d passed to the service, %d rejected with 429", ok.Load(), limited.Load())
	if limited.Load() == 0 {
		t.Error("no request was rate limited")
	}
	if ok.Load() == 0 {
		t.Error("all requests were rate limited")
	}
	time.Sleep(2 * time.Second)
}
