package cache

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

type user struct {
	ID   int64
	Name string
}

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

func newCache(t *testing.T) (*miniredis.Miniredis, *Cache[user]) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return mr, New[user](rdb, Options{TTL: time.Minute, NullTTL: 10 * time.Second}, discard)
}

func TestGetLoadsOnceThenHits(t *testing.T) {
	_, c := newCache(t)
	var calls atomic.Int32
	load := func(context.Context) (user, error) {
		calls.Add(1)
		return user{ID: 1, Name: "a"}, nil
	}
	for range 3 {
		u, err := c.Get(context.Background(), "u:1", load)
		if err != nil || u.Name != "a" {
			t.Fatalf("Get = %+v, %v", u, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("loader called %d times, want 1", calls.Load())
	}
}

// 穿透防护：不存在的数据写入空值标记，后续请求不再回源。
func TestNotFoundIsCached(t *testing.T) {
	mr, c := newCache(t)
	var calls atomic.Int32
	load := func(context.Context) (user, error) {
		calls.Add(1)
		return user{}, ErrNotFound
	}
	for range 3 {
		if _, err := c.Get(context.Background(), "u:404", load); !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("loader called %d times, want 1", calls.Load())
	}
	if ttl := mr.TTL("u:404"); ttl != 10*time.Second {
		t.Fatalf("null ttl = %s, want 10s", ttl)
	}
}

// 击穿防护：同一个键的并发未命中只回源一次。
func TestConcurrentMissesLoadOnce(t *testing.T) {
	_, c := newCache(t)
	var calls atomic.Int32
	release := make(chan struct{})
	load := func(context.Context) (user, error) {
		calls.Add(1)
		<-release
		return user{ID: 1}, nil
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Get(context.Background(), "hot", load); err != nil {
				t.Error(err)
			}
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("loader called %d times, want 1", calls.Load())
	}
}

// 雪崩防护：TTL 带抖动，落在 ±10% 范围内。
func TestTTLJitter(t *testing.T) {
	mr, c := newCache(t)
	load := func(context.Context) (user, error) { return user{ID: 1}, nil }
	if _, err := c.Get(context.Background(), "u:1", load); err != nil {
		t.Fatal(err)
	}
	ttl := mr.TTL("u:1")
	if ttl < 54*time.Second || ttl > 66*time.Second {
		t.Fatalf("ttl = %s, want within [54s, 66s]", ttl)
	}
}

// Redis 不可用时直接回源，不返回错误。
func TestRedisDownFallsBackToLoader(t *testing.T) {
	mr, c := newCache(t)
	mr.Close()
	u, err := c.Get(context.Background(), "u:1", func(context.Context) (user, error) {
		return user{ID: 1, Name: "db"}, nil
	})
	if err != nil || u.Name != "db" {
		t.Fatalf("Get = %+v, %v; want value from loader", u, err)
	}
}

func TestLoaderErrorNotCached(t *testing.T) {
	mr, c := newCache(t)
	_, err := c.Get(context.Background(), "u:1", func(context.Context) (user, error) {
		return user{}, errors.New("db down")
	})
	if err == nil {
		t.Fatal("want loader error")
	}
	if mr.Exists("u:1") {
		t.Fatal("loader error must not be cached")
	}
}

func TestGetMany(t *testing.T) {
	mr, c := newCache(t)
	ctx := context.Background()
	if _, err := c.Get(ctx, "u:1", func(context.Context) (user, error) { return user{ID: 1}, nil }); err != nil {
		t.Fatal(err)
	}
	var gotMissing []string
	out, err := c.GetMany(ctx, []string{"u:1", "u:2", "u:3"}, func(_ context.Context, missing []string) (map[string]user, error) {
		gotMissing = missing
		return map[string]user{"u:2": {ID: 2}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(gotMissing) != 2 || gotMissing[0] != "u:2" || gotMissing[1] != "u:3" {
		t.Fatalf("missing = %v, want [u:2 u:3]", gotMissing)
	}
	if len(out) != 2 || out["u:1"].ID != 1 || out["u:2"].ID != 2 {
		t.Fatalf("out = %+v", out)
	}
	// u:3 不存在，应写入空值标记，第二次批量读取不再回源
	if !mr.Exists("u:3") {
		t.Fatal("u:3 null marker not written")
	}
	_, err = c.GetMany(ctx, []string{"u:3"}, func(context.Context, []string) (map[string]user, error) {
		t.Fatal("loader called for cached null")
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDelete(t *testing.T) {
	mr, c := newCache(t)
	ctx := context.Background()
	c.Get(ctx, "u:1", func(context.Context) (user, error) { return user{ID: 1}, nil })
	if err := c.Delete(ctx, "u:1"); err != nil {
		t.Fatal(err)
	}
	if mr.Exists("u:1") {
		t.Fatal("key still exists after Delete")
	}
}
