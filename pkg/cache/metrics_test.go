package cache

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/attribute"

	"github.com/Mars-08042/MarsChat/pkg/telemetry/telemetrytest"
)

func requests(t *testing.T, name, result string) int64 {
	return telemetrytest.Sum(t, "marschat_cache_requests",
		attribute.String("cache", name), attribute.String("result", result))
}

func loads(t *testing.T, name, result string) int64 {
	return telemetrytest.Sum(t, "marschat_cache_loads",
		attribute.String("cache", name), attribute.String("result", result))
}

// 指标按结果计数：并发未命中只回源一次，miss 与 loads 的差就是 singleflight 合并掉的回源。
func TestMetrics(t *testing.T) {
	telemetrytest.Reader(t)
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	c := New[user](rdb, Options{Name: "metrics_test", TTL: time.Minute, NullTTL: 10 * time.Second}, discard)
	ctx := context.Background()

	release := make(chan struct{})
	load := func(context.Context) (user, error) {
		<-release
		return user{ID: 1}, nil
	}
	const n = 20
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() { _, _ = c.Get(ctx, "u:1", load) })
	}
	// 等所有请求都读过 Redis 并进入 singleflight 后再放行回源
	deadline := time.Now().Add(2 * time.Second)
	for requests(t, "metrics_test", "miss") < n && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	close(release)
	wg.Wait()

	if got := requests(t, "metrics_test", "miss"); got != n {
		t.Errorf("miss = %d, want %d", got, n)
	}
	if got := loads(t, "metrics_test", "ok"); got != 1 {
		t.Errorf("loads ok = %d, want 1 (singleflight)", got)
	}

	if _, err := c.Get(ctx, "u:1", load); err != nil {
		t.Fatal(err)
	}
	if got := requests(t, "metrics_test", "hit"); got != 1 {
		t.Errorf("hit = %d, want 1", got)
	}

	notFound := func(context.Context) (user, error) { return user{}, ErrNotFound }
	for range 2 {
		if _, err := c.Get(ctx, "u:404", notFound); !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	}
	if got := loads(t, "metrics_test", "not_found"); got != 1 {
		t.Errorf("loads not_found = %d, want 1", got)
	}
	if got := requests(t, "metrics_test", "null_hit"); got != 1 {
		t.Errorf("null_hit = %d, want 1", got)
	}

	// 到这里 miss 有 n+1 次：并发的 n 次加 u:404 第一次读取。
	// 批量读：u:1 命中、u:404 命中空值标记、u:2 未命中回源一次
	got, err := c.GetMany(ctx, []string{"u:1", "u:404", "u:2"}, func(_ context.Context, missing []string) (map[string]user, error) {
		return map[string]user{"u:2": {ID: 2}}, nil
	})
	if err != nil || len(got) != 2 {
		t.Fatalf("GetMany = %v, %v", got, err)
	}
	if h, nh, m := requests(t, "metrics_test", "hit"), requests(t, "metrics_test", "null_hit"), requests(t, "metrics_test", "miss"); h != 2 || nh != 2 || m != n+2 {
		t.Errorf("after GetMany hit=%d null_hit=%d miss=%d, want 2, 2, %d", h, nh, m, n+2)
	}
	if got := loads(t, "metrics_test", "ok"); got != 2 {
		t.Errorf("loads ok = %d, want 2", got)
	}

	// Redis 不可用：按 error 计数，仍回源
	mr.Close()
	if _, err := c.Get(ctx, "u:3", func(context.Context) (user, error) { return user{ID: 3}, nil }); err != nil {
		t.Fatalf("Get with redis down: %v", err)
	}
	if got := requests(t, "metrics_test", "error"); got != 1 {
		t.Errorf("error = %d, want 1", got)
	}
}
