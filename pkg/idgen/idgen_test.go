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

// 节点号被其他实例接手后，续期不能把它抢回来，而是换一个空闲节点号继续发号（TD-34）。
func TestRenewDoesNotStealTakenNode(t *testing.T) {
	mr, rdb := newTestRedis(t)
	g, cleanup, err := New(context.Background(), rdb, "a", discard)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	oldNode, oldKey := g.Node(), g.key()
	if err := mr.Set(oldKey, "b"); err != nil {
		t.Fatal(err)
	}
	g.renew()
	if got, _ := mr.Get(oldKey); got != "b" {
		t.Fatalf("old node owner = %q, want b", got)
	}
	if g.Node() == oldNode {
		t.Fatal("generator still uses the node taken by another instance")
	}
	if got, _ := mr.Get(g.key()); got != "a" {
		t.Fatalf("new node owner = %q, want a", got)
	}
	id, err := g.NextID()
	if err != nil {
		t.Fatalf("NextID after switching node: %v", err)
	}
	if _, node, _ := Decompose(id); node != g.Node() {
		t.Fatalf("id node = %d, want %d", node, g.Node())
	}
}

// Redis 停机超过 TTL 后重启，租约键已经过期：发号先失败，续期时重新获取节点号后恢复（TD-34）。
func TestLeaseExpiredInRedisIsReacquired(t *testing.T) {
	mr, rdb := newTestRedis(t)
	g, cleanup, err := New(context.Background(), rdb, "a", discard)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	// 模拟停机期间续期失败、本地租约到期、Redis 中的键过期
	base := time.Now()
	g.now = func() time.Time { return base.Add(leaseTTL + time.Second) }
	mr.FastForward(leaseTTL + time.Second)
	if mr.Exists(g.key()) {
		t.Fatal("node key should have expired")
	}
	if _, err := g.NextID(); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("err = %v, want ErrLeaseLost while the lease is lost", err)
	}

	g.renew()
	if !mr.Exists(g.key()) {
		t.Fatal("no node reacquired")
	}
	if _, err := g.NextID(); err != nil {
		t.Fatalf("NextID after reacquire: %v", err)
	}
}

// Redis 不可用时续期失败，本地租约到期前照常发号，不换节点号。
func TestRenewErrorKeepsNodeUntilDeadline(t *testing.T) {
	mr, rdb := newTestRedis(t)
	g, cleanup, err := New(context.Background(), rdb, "a", discard)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	node := g.Node()
	mr.SetError("LOADING Redis is loading the dataset in memory")
	g.renew()
	mr.SetError("")
	if g.Node() != node {
		t.Fatal("node changed on a transient renew error")
	}
	if _, err := g.NextID(); err != nil {
		t.Fatalf("NextID within the local deadline: %v", err)
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
