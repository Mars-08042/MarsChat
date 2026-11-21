// Package testkit 是端到端测试（test/e2e）与集群测试（test/cluster）共用的客户端辅助函数：
// 按 protobuf JSON 调用 HTTP 接口、注册用户、收发 WebSocket 帧、读取 Redis 中的状态。
// 两组测试的区别只在地址：e2e 直连各服务端口，集群测试全部经网关。只供测试使用。
package testkit

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	accountv1 "github.com/Mars-08042/MarsChat/api/account/v1"
)

// 依赖本地配置：account 使用 log 发送方和默认固定验证码 000000（app/account/configs/config.yaml）。
const (
	FixedCode = "000000"
	Password  = "abc12345"
)

// APIError 是 Kratos 错误的 JSON 响应体。
type APIError struct {
	Code     int               `json:"code"`
	Reason   string            `json:"reason"`
	Message  string            `json:"message"`
	Metadata map[string]string `json:"metadata"`
}

// Call 按 protobuf JSON 调用 base 地址上的接口，与浏览器客户端一致。返回 HTTP 状态码和错误体（成功时为 nil）。
// 非 200 且响应体不是 Kratos 错误格式（例如网关限流返回的 {"error_msg": ...}）时 Code 为状态码、Reason 为空。
func Call(t testing.TB, base, method, path, token string, req, resp proto.Message) (int, *APIError) {
	t.Helper()
	var body io.Reader
	if req != nil {
		b, err := protojson.Marshal(req)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(b)
	}
	r, err := http.NewRequest(method, base+path, body)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		e := APIError{Code: res.StatusCode}
		if err := json.Unmarshal(data, &e); err != nil {
			t.Fatalf("%s %s: status %d, body %s", method, path, res.StatusCode, data)
		}
		return res.StatusCode, &e
	}
	if resp != nil {
		if err := protojson.Unmarshal(data, resp); err != nil {
			t.Fatalf("%s %s: decode %s: %v", method, path, data, err)
		}
	}
	return res.StatusCode, nil
}

// MustCall 调用接口，失败时终止测试。
func MustCall(t testing.TB, base, method, path, token string, req, resp proto.Message) {
	t.Helper()
	if status, e := Call(t, base, method, path, token, req, resp); e != nil {
		t.Fatalf("%s %s: status %d, %+v", method, path, status, e)
	}
}

// RandomEmail 生成随机的测试邮箱，避免与已注册用户冲突。域名用保留顶级域 .test，
// 即使 account 误配成 smtp 发送方也不会真的发出邮件（发送方拒绝保留域名）。
func RandomEmail(t testing.TB) string {
	t.Helper()
	n, err := rand.Int(rand.Reader, big.NewInt(1<<48))
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("e2e-%012x@example.test", n.Int64())
}

// LocalPart 返回邮箱 @ 之前的部分，只含字母、数字和连字符，可以拼进设备 ID。
func LocalPart(email string) string {
	local, _, _ := strings.Cut(email, "@")
	return local
}

// Device 构造登录设备信息。
func Device(id string, p accountv1.Platform) *accountv1.Device {
	return &accountv1.Device{DeviceId: id, Platform: p, DeviceName: "test " + id}
}

// Session 是注册或登录的结果。
type Session struct {
	User  *accountv1.User
	Token *accountv1.TokenPair
}

// Register 用固定验证码注册新用户，accountBase 是 account 的 HTTP 地址（直连或网关）。
func Register(t testing.TB, accountBase, email, deviceID string) Session {
	t.Helper()
	MustCall(t, accountBase, http.MethodPost, "/v1/auth/email-codes", "",
		&accountv1.SendEmailCodeRequest{Email: email, Purpose: accountv1.CodePurpose_CODE_PURPOSE_REGISTER}, nil)
	var resp accountv1.RegisterResponse
	MustCall(t, accountBase, http.MethodPost, "/v1/auth/register", "", &accountv1.RegisterRequest{
		Email: email, Code: FixedCode, Password: Password, Nickname: "小克",
		Device: Device(deviceID, accountv1.Platform_PLATFORM_WEB),
	}, &resp)
	return Session{User: resp.GetUser(), Token: resp.GetToken()}
}

// NewRedis 连接本地 Redis（宿主机端口），密码取自 MARSCHAT_REDIS_PASSWORD（make e2e 会加载 .env）。
func NewRedis(t testing.TB) *redis.Client {
	t.Helper()
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:16379", Password: os.Getenv("MARSCHAT_REDIS_PASSWORD")})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

// ResetRateLimits 清除 account 对某个客户端 IP 的限流计数，否则多次运行会触发每小时 20 次的发码上限。
// 直连时 IP 为 127.0.0.1；经网关时为 Docker 网络的网关地址（见 GatewayClientIP）。
func ResetRateLimits(t testing.TB, rdb *redis.Client, ip string) {
	t.Helper()
	if err := rdb.Del(context.Background(), "account:rl:email_code_ip:"+ip, "account:rl:login_ip:"+ip).Err(); err != nil {
		t.Fatalf("reset rate limits (is MARSCHAT_REDIS_PASSWORD set? run via make): %v", err)
	}
}

// RouteFields 返回用户的在线路由（连接 ID → "会话ID|access 地址"）。
func RouteFields(t testing.TB, rdb *redis.Client, userID int64) map[string]string {
	t.Helper()
	m, err := rdb.HGetAll(context.Background(), "access:route:"+strconv.FormatInt(userID, 10)).Result()
	if err != nil {
		t.Fatal(err)
	}
	return m
}
