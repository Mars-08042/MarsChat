package idgen

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return mr, rdb
}

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestNextIDUniqueAndIncreasing(t *testing.T) {
	_, rdb := newTestRedis(t)
	g, cleanup, err := New(context.Background(), rdb, "test-1", discard)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	const workers, perWorker = 8, 5000
	var mu sync.Mutex
	seen := make(map[int64]struct{}, workers*perWorker)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			last := int64(0)
			for range perWorker {
				id, err := g.NextID()
				if err != nil {
					t.Error(err)
					return
				}
				if id <= last {
					t.Errorf("id not increasing within goroutine: %d after %d", id, last)
				}
				last = id
				mu.Lock()
				seen[id] = struct{}{}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(seen) != workers*perWorker {
		t.Fatalf("got %d unique ids, want %d", len(seen), workers*perWorker)
	}
}

func TestDecompose(t *testing.T) {
	_, rdb := newTestRedis(t)
	g, cleanup, err := New(context.Background(), rdb, "test-1", discard)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	id, err := g.NextID()
	if err != nil {
		t.Fatal(err)
	}
	ts, node, _ := Decompose(id)
	if node != g.Node() {
		t.Errorf("node = %d, want %d", node, g.Node())
	}
	if d := time.Since(ts); d < 0 || d > time.Minute {
		t.Errorf("timestamp %s too far from now", ts)
	}
}

func TestTwoGeneratorsGetDifferentNodes(t *testing.T) {
	_, rdb := newTestRedis(t)
	g1, c1, err := New(context.Background(), rdb, "a", discard)
	if err != nil {
		t.Fatal(err)
	}
	defer c1()
	g2, c2, err := New(context.Background(), rdb, "b", discard)
	if err != nil {
		t.Fatal(err)
	}
	defer c2()
	if g1.Node() == g2.Node() {
		t.Fatalf("both generators got node %d", g1.Node())
	}
}

func TestCleanupReleasesNode(t *testing.T) {
	mr, rdb := newTestRedis(t)
	g, cleanup, err := New(context.Background(), rdb, "a", discard)
	if err != nil {
		t.Fatal(err)
	}
	key := g.key()
	cleanup()
	if mr.Exists(key) {
		t.Fatalf("node key %s still exists after cleanup", key)
	}
}

// 本地租约截止时间过后必须停止发号，即使进程还活着。
func TestNextIDFailsAfterLeaseExpires(t *testing.T) {
	_, rdb := newTestRedis(t)
	g, cleanup, err := New(context.Background(), rdb, "a", discard)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	base := time.Now()
	g.now = func() time.Time { return base.Add(leaseTTL + time.Second) }
	if _, err := g.NextID(); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("err = %v, want ErrLeaseLost", err)
	}
}

// 节点号被其他实例接手后，续期不能把它抢回来。
func TestRenewDoesNotStealTakenNode(t *testing.T) {
	mr, rdb := newTestRedis(t)
	g, cleanup, err := New(context.Background(), rdb, "a", discard)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	before := g.leaseUntil.Load()
	if err := mr.Set(g.key(), "b"); err != nil {
		t.Fatal(err)
	}
	g.renew()
	if got, _ := mr.Get(g.key()); got != "b" {
		t.Fatalf("node owner = %q, want b", got)
	}
	if g.leaseUntil.Load() != before {
		t.Fatal("lease extended although node is owned by another instance")
	}
}

func TestClockBackwards(t *testing.T) {
	_, rdb := newTestRedis(t)
	g, cleanup, err := New(context.Background(), rdb, "a", discard)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	base := time.Now()
	g.now = func() time.Time { return base }
	if _, err := g.NextID(); err != nil {
		t.Fatal(err)
	}
	g.now = func() time.Time { return base.Add(-time.Second) }
	if _, err := g.NextID(); !errors.Is(err, ErrClockBackwards) {
		t.Fatalf("err = %v, want ErrClockBackwards", err)
	}
}
