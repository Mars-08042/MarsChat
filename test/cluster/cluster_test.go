//go:build cluster

// Package cluster 是容器模式下的集群测试与故障实验：客户端只经网关访问，核对状态时直接读 Redis 的宿主机端口。
// 需要先 make cluster-up（依赖组件、迁移、镜像都已就绪），用 make cluster-test 运行。
package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	socialv1 "github.com/Mars-08042/MarsChat/api/social/v1"
	"github.com/Mars-08042/MarsChat/test/testkit"
)

const (
	gatewayHTTP  = "http://127.0.0.1:19080"
	gatewayHTTPS = "https://localhost:19443"
	gatewayWS    = "ws://127.0.0.1:19080/ws"
	consulHTTP   = "http://127.0.0.1:18500"
	// 本机请求经 Docker Desktop 端口转发到网关时的来源地址，即 compose 网络的网关（ip_range 的第一个地址）。
	// account 信任网关转发的 X-Forwarded-For，看到的客户端 IP 就是它。
	gatewayClientIP = "172.30.0.128"
)

// TestMain 确认测试面对的是容器模式：宿主机运行的实例注册的是 127.0.0.1，网关和容器都访问不到。
func TestMain(m *testing.M) {
	for _, svc := range []string{"account", "social", "message", "access", "delivery", "media"} {
		addrs, err := passingAddrs("marschat-" + svc)
		if err != nil {
			fmt.Fprintf(os.Stderr, "query consul: %v (is make cluster-up running?)\n", err)
			os.Exit(1)
		}
		if len(addrs) == 0 {
			fmt.Fprintf(os.Stderr, "no passing instance of marschat-%s, run make cluster-up first\n", svc)
			os.Exit(1)
		}
		for _, a := range addrs {
			if strings.HasPrefix(a, "127.0.0.1") {
				fmt.Fprintf(os.Stderr, "marschat-%s has a host instance (%s); stop host services before cluster tests\n", svc, a)
				os.Exit(1)
			}
		}
	}
	os.Exit(m.Run())
}

// passingAddrs 返回服务在 Consul 中检查通过的实例地址（Address:Port）。
func passingAddrs(service string) ([]string, error) {
	res, err := http.Get(consulHTTP + "/v1/health/service/" + service + "?passing=true")
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var entries []struct {
		Service struct {
			Address string
			Port    int
		}
	}
	if err := json.NewDecoder(res.Body).Decode(&entries); err != nil {
		return nil, err
	}
	addrs := make([]string, 0, len(entries))
	for _, e := range entries {
		addrs = append(addrs, e.Service.Address+":"+strconv.Itoa(e.Service.Port))
	}
	return addrs, nil
}

type user struct {
	id    int64
	token string
}

func newUser(t *testing.T) user {
	t.Helper()
	email := testkit.RandomEmail(t)
	s := testkit.Register(t, gatewayHTTP, email, "cluster-"+testkit.LocalPart(email))
	return user{id: s.User.GetId(), token: s.Token.GetAccessToken()}
}

func makeFriends(t *testing.T, a, b user) {
	t.Helper()
	var sent socialv1.SendFriendRequestResponse
	testkit.MustCall(t, gatewayHTTP, http.MethodPost, "/v1/friend-requests", a.token, &socialv1.SendFriendRequestRequest{UserId: b.id}, &sent)
	testkit.MustCall(t, gatewayHTTP, http.MethodPost,
		"/v1/friend-requests/"+strconv.FormatInt(sent.GetRequest().GetId(), 10)+"/accept", b.token, &socialv1.AcceptFriendRequestRequest{}, nil)
}

// resetRateLimits 清除 account 对本机（经网关后的来源地址）的限流计数。
func resetRateLimits(t *testing.T) {
	t.Helper()
	testkit.ResetRateLimits(t, testkit.NewRedis(t), gatewayClientIP)
}

// conn 是经网关建立并认证过的连接，addr 是它所在 access 实例的 gRPC 地址（取自在线路由）。
type conn struct {
	*testkit.WS
	id   int64
	addr string
}

// connect 经网关建立连接并认证，从在线路由中读出它落在哪个 access 实例。
func connect(t *testing.T, u user) conn {
	t.Helper()
	c := testkit.Dial(t, gatewayWS)
	ok := c.Auth(u.token)
	field := strconv.FormatInt(ok.GetConnectionId(), 10)
	rdb := testkit.NewRedis(t)
	deadline := time.Now().Add(3 * time.Second)
	for {
		if v := testkit.RouteFields(t, rdb, u.id)[field]; v != "" {
			return conn{WS: c, id: ok.GetConnectionId(), addr: v[strings.LastIndex(v, "|")+1:]}
		}
		if time.Now().After(deadline) {
			t.Fatalf("route of connection %d not registered", ok.GetConnectionId())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// connectWhere 反复建立连接，直到落在满足 want 的 access 实例上；不满足的连接立即关闭。
func connectWhere(t *testing.T, u user, what string, want func(addr string) bool) conn {
	t.Helper()
	for i := 0; i < 20; i++ {
		c := connect(t, u)
		if want(c.addr) {
			return c
		}
		_ = c.Conn.Close()
	}
	t.Fatalf("no connection landed on %s after 20 attempts", what)
	return conn{}
}

func ctx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return c
}
