//go:build e2e

package e2e

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestMetricsEndpoints 检查各服务的 HTTP 端口都暴露 /metrics：Go 运行时指标、
// Kratos 请求指标（account 先处理一个请求）和 delivery 的 Kafka 积压指标。
func TestMetricsEndpoints(t *testing.T) {
	// 未带令牌访问 /v1/users/me 返回 401，请求经过 metrics 中间件，account 才有请求指标
	if status, _ := call(t, http.MethodGet, "/v1/users/me", "", nil, nil); status != http.StatusUnauthorized {
		t.Fatalf("GET /v1/users/me without token: status %d, want 401", status)
	}
	cases := []struct {
		name string
		base string
		want []string
	}{
		{"access", "http://127.0.0.1:18000", nil},
		{"account", accountHTTP, []string{"server_requests_code_total", "server_requests_seconds_bucket"}},
		{"social", socialHTTP, nil},
		{"message", messageHTTP, nil},
		{"delivery", "http://127.0.0.1:18040", []string{"marschat_kafka_consumer_lag"}},
		{"media", mediaHTTP, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := http.Get(c.base + "/metrics")
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			body, err := io.ReadAll(res.Body)
			if err != nil {
				t.Fatal(err)
			}
			if res.StatusCode != http.StatusOK {
				t.Fatalf("status %d", res.StatusCode)
			}
			for _, name := range append([]string{"go_goroutines", "target_info"}, c.want...) {
				if !strings.Contains(string(body), name) {
					t.Errorf("/metrics has no %s", name)
				}
			}
		})
	}
}
