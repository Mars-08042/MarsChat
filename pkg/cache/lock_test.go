package cache

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newLockedCache(t *testing.T) (*miniredis.Miniredis, *Cache[user]) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return mr, New[user](rdb, Options{TTL: time.Minute, NullTTL: 10 * time.Second, RebuildLock: true}, discard)
}

// 抢到锁的实例回源、回填，结束后释放锁。
func TestRebuildLockAcquiredLoadsAndReleases(t *testing.T) {
	mr, c := newLockedCache(t)
	var lockHeld bool
	u, err := c.Get(context.Background(), "g:1", func(context.Context) (user, error) {
		lockHeld = mr.Exists("g:1:lock")
		return user{ID: 1}, nil
	})
	if err != nil || u.ID != 1 {
		t.Fatalf("Get = %+v, %v", u, err)
	}
	if !lockHeld {
		t.Fatal("lock not held while loading")
	}
	if mr.Exists("g:1:lock") {
		t.Fatal("lock not released after load")
	}
	if !mr.Exists("g:1") {
		t.Fatal("value not filled")
	}
}

// 锁被别的实例持有，且它在等待期间回填：不回源，直接用回填的值。
func TestRebuildLockWaitsForOtherInstance(t *testing.T) {
	mr, c := newLockedCache(t)
	mr.Set("g:1:lock", "other")
	go func() {
		time.Sleep(60 * time.Millisecond)
		mr.Set("g:1", string(append([]byte{markerValue}, `{"ID":1,"Name":"filled"}`...)))
	}()
	u, err := c.Get(context.Background(), "g:1", func(context.Context) (user, error) {
		t.Error("loader called while another instance holds the lock")
		return user{}, nil
	})
	if err != nil || u.Name != "filled" {
		t.Fatalf("Get = %+v, %v; want value filled by the lock holder", u, err)
	}
	if got, _ := mr.Get("g:1:lock"); got != "other" {
		t.Fatalf("lock owned by another instance was changed: %q", got)
	}
}

// 别的实例回填的是空值标记：返回 ErrNotFound，也不回源。
func TestRebuildLockWaitSeesNullMarker(t *testing.T) {
	mr, c := newLockedCache(t)
	mr.Set("g:404:lock", "other")
	go func() {
		time.Sleep(60 * time.Millisecond)
		mr.Set("g:404", string([]byte{markerNull}))
	}()
	_, err := c.Get(context.Background(), "g:404", func(context.Context) (user, error) {
		t.Error("loader called while another instance holds the lock")
		return user{}, nil
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// 等不到回填（持锁实例卡住或崩溃）：大约 200 毫秒后直接回源，不返回错误。
func TestRebuildLockTimeoutFallsBackToLoader(t *testing.T) {
	mr, c := newLockedCache(t)
	mr.Set("g:1:lock", "other")
	var calls atomic.Int32
	start := time.Now()
	u, err := c.Get(context.Background(), "g:1", func(context.Context) (user, error) {
		calls.Add(1)
		return user{ID: 1, Name: "db"}, nil
	})
	elapsed := time.Since(start)
	if err != nil || u.Name != "db" || calls.Load() != 1 {
		t.Fatalf("Get = %+v, %v, loader calls %d", u, err, calls.Load())
	}
	if elapsed < lockPollInterval*lockPolls {
		t.Fatalf("loaded after %s, want to wait at least %s", elapsed, lockPollInterval*lockPolls)
	}
	if got, _ := mr.Get("g:1:lock"); got != "other" {
		t.Fatalf("lock owned by another instance was changed: %q", got)
	}
}

// 回源超过锁的有效期，锁已被别的实例重新抢到时，不能误删别人的锁。
func TestRebuildLockReleaseChecksOwner(t *testing.T) {
	mr, c := newLockedCache(t)
	_, err := c.Get(context.Background(), "g:1", func(context.Context) (user, error) {
		mr.Set("g:1:lock", "other")
		return user{ID: 1}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := mr.Get("g:1:lock"); got != "other" {
		t.Fatalf("lock owned by another instance was released: %q", got)
	}
}
