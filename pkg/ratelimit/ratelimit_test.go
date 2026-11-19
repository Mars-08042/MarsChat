package ratelimit

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newLimiter(t *testing.T) (*miniredis.Miniredis, *Limiter) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return mr, New(rdb)
}

func TestAllowWithinLimit(t *testing.T) {
	_, l := newLimiter(t)
	ctx := context.Background()
	for i := range 3 {
		r, err := l.Allow(ctx, "k", 3, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if !r.Allowed {
			t.Fatalf("request %d rejected, want allowed", i+1)
		}
	}
	r, err := l.Allow(ctx, "k", 3, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if r.Allowed {
		t.Fatal("4th request allowed, want rejected")
	}
	if r.RetryAfter <= 0 || r.RetryAfter > time.Minute {
		t.Fatalf("RetryAfter = %s, want within (0, 1m]", r.RetryAfter)
	}
}

func TestKeysAreIndependent(t *testing.T) {
	_, l := newLimiter(t)
	ctx := context.Background()
	if r, _ := l.Allow(ctx, "a", 1, time.Minute); !r.Allowed {
		t.Fatal("first request on a rejected")
	}
	if r, _ := l.Allow(ctx, "b", 1, time.Minute); !r.Allowed {
		t.Fatal("first request on b rejected")
	}
}

// 窗口滑过后，最早的请求不再计数。
func TestWindowSlides(t *testing.T) {
	mr, l := newLimiter(t)
	ctx := context.Background()
	base := time.Now()
	mr.SetTime(base)
	if r, _ := l.Allow(ctx, "k", 1, time.Minute); !r.Allowed {
		t.Fatal("first request rejected")
	}
	mr.SetTime(base.Add(30 * time.Second))
	if r, _ := l.Allow(ctx, "k", 1, time.Minute); r.Allowed {
		t.Fatal("request inside window allowed")
	}
	mr.SetTime(base.Add(61 * time.Second))
	if r, _ := l.Allow(ctx, "k", 1, time.Minute); !r.Allowed {
		t.Fatal("request after window rejected")
	}
}

// 被拒绝的请求不计入窗口，否则持续请求的客户端永远无法恢复。
func TestRejectedRequestsNotCounted(t *testing.T) {
	mr, l := newLimiter(t)
	ctx := context.Background()
	base := time.Now()
	mr.SetTime(base)
	l.Allow(ctx, "k", 1, time.Minute)
	for range 5 {
		l.Allow(ctx, "k", 1, time.Minute)
	}
	if n, _ := mr.ZMembers("k"); len(n) != 1 {
		t.Fatalf("window has %d members, want 1", len(n))
	}
}
