package ratelimit

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/Mars-08042/MarsChat/pkg/telemetry/telemetrytest"
)

func TestLimitName(t *testing.T) {
	cases := map[string]string{
		"message:rl:send:42":              "send",
		"account:rl:login_ip:2001:db8::1": "login_ip",
		"k":                               "other",
		"account:rl::1":                   "other",
		"account:cache:user:1":            "other",
	}
	for key, want := range cases {
		if got := limitName(key); got != want {
			t.Errorf("limitName(%q) = %q, want %q", key, got, want)
		}
	}
}

// 放行与拒绝分别计数，标签只有名字，不含用户 ID。
func TestDecisionMetrics(t *testing.T) {
	telemetrytest.Reader(t)
	_, l := newLimiter(t)
	ctx := context.Background()
	for range 3 {
		if _, err := l.Allow(ctx, "metrics:rl:metrics_test:42", 2, time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	name := attribute.String("name", "metrics_test")
	if got := telemetrytest.Sum(t, "marschat_ratelimit", name, attribute.String("result", "allowed")); got != 2 {
		t.Errorf("allowed = %d, want 2", got)
	}
	if got := telemetrytest.Sum(t, "marschat_ratelimit", name, attribute.String("result", "rejected")); got != 1 {
		t.Errorf("rejected = %d, want 1", got)
	}
}
