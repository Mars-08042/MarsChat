//go:build e2e

package e2e

import (
	"context"
	"crypto/rand"
	"io"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Mars-08042/MarsChat/pkg/bloom"
)

// pkg/bloom 的失败分支需要真实的 RedisBloom（miniredis 不支持 BF.* 命令），见 TD-27。
// 每个测试用独立的键前缀，结束后清理。错误注入的方式：把过滤器的键改成字符串，BF.INSERT 会报 WRONGTYPE。

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// fakeTable 模拟被扫描的表，onScan 在每轮重建开始扫描时调用（after == 0）。
type fakeTable struct {
	mu     sync.Mutex
	ids    []int64
	rounds atomic.Int32
	onScan func()
}

func (f *fakeTable) add(id int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ids = append(f.ids, id)
	slices.Sort(f.ids)
}

func (f *fakeTable) scan(_ context.Context, after int64, limit int) ([]int64, error) {
	if after == 0 {
		f.rounds.Add(1)
		if f.onScan != nil {
			f.onScan()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []int64
	for _, id := range f.ids {
		if id > after && len(out) < limit {
			out = append(out, id)
		}
	}
	return out, nil
}

type bloomEnv struct {
	rdb   *redis.Client
	key   string
	table *fakeTable
	f     *bloom.Filter
}

func newBloomEnv(t *testing.T, ids ...int64) *bloomEnv {
	t.Helper()
	rdb := newRedisClient(t)
	e := &bloomEnv{rdb: rdb, key: "e2e:bloom:" + rand.Text(), table: &fakeTable{}}
	for _, id := range ids {
		e.table.add(id)
	}
	t.Cleanup(func() {
		rdb.Del(context.Background(), e.key, e.key+":ready", e.key+":dirty", e.key+":rebuild")
	})
	e.f = bloom.New(rdb, bloom.Options{
		Key: e.key, Capacity: 1000, ErrorRate: 0.001, Scan: e.table.scan,
		AddRetry:       []time.Duration{100 * time.Millisecond, 100 * time.Millisecond},
		RebuildBackoff: 10 * time.Millisecond,
	}, quiet)
	return e
}

func (e *bloomEnv) exists(t *testing.T, key string) bool {
	t.Helper()
	n, err := e.rdb.Exists(context.Background(), key).Result()
	if err != nil {
		t.Fatal(err)
	}
	return n == 1
}

// waitReady 等待后台重建完成（就绪标记出现）。
func (e *bloomEnv) waitReady(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !e.exists(t, e.key+":ready") {
		if time.Now().After(deadline) {
			t.Fatal("bloom filter not ready within 5s")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (e *bloomEnv) contains(t *testing.T, id int64) bool {
	t.Helper()
	v, err := e.rdb.Do(context.Background(), "BF.EXISTS", e.key, id).Bool()
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestBloomRebuildAndLookup(t *testing.T) {
	e := newBloomEnv(t, 1, 2, 3)
	ctx := context.Background()
	// 未就绪时一律放行，由调用方查库
	if !e.f.MightExist(ctx, 404) {
		t.Fatal("not-ready filter must return true")
	}
	e.waitReady(t)
	if !e.f.MightExist(ctx, 2) || e.f.MightExist(ctx, 404) {
		t.Fatal("ready filter gave wrong answers")
	}
	e.f.Add(ctx, 4)
	if !e.f.MightExist(ctx, 4) {
		t.Fatal("added id not found")
	}
}

// 加入失败后重试成功：不写脏标记，不触发重建。
func TestBloomAddRetrySucceeds(t *testing.T) {
	e := newBloomEnv(t, 1)
	ctx := context.Background()
	e.f.EnsureReady(ctx)
	e.waitReady(t)
	rounds := e.table.rounds.Load()

	if err := e.rdb.Set(ctx, e.key, "broken", 0).Err(); err != nil {
		t.Fatal(err)
	}
	e.f.Add(ctx, 2)
	// 第一次重试之前修复：重试时 BF.INSERT 重新建出过滤器
	if err := e.rdb.Del(ctx, e.key).Err(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !e.exists(t, e.key) || !e.contains(t, 2) {
		if time.Now().After(deadline) {
			t.Fatal("id not added by retry")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if e.exists(t, e.key+":dirty") || !e.exists(t, e.key+":ready") || e.table.rounds.Load() != rounds {
		t.Fatal("successful retry must not mark dirty or rebuild")
	}
}

// 重试仍失败：写脏标记、删除就绪标记并重建，重建后包含新 ID。
func TestBloomAddRetryExhaustedRebuilds(t *testing.T) {
	e := newBloomEnv(t, 1)
	ctx := context.Background()
	e.f.EnsureReady(ctx)
	e.waitReady(t)

	if err := e.rdb.Set(ctx, e.key, "broken", 0).Err(); err != nil {
		t.Fatal(err)
	}
	e.table.add(2) // 数据库已提交
	e.f.Add(ctx, 2)
	// 重试耗尽后就绪标记被删除，重建完成后重新出现
	deadline := time.Now().Add(5 * time.Second)
	for e.table.rounds.Load() < 2 || !e.exists(t, e.key+":ready") {
		if time.Now().After(deadline) {
			t.Fatalf("not rebuilt after retries exhausted (rounds %d)", e.table.rounds.Load())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !e.contains(t, 1) || !e.contains(t, 2) || e.exists(t, e.key+":dirty") {
		t.Fatal("rebuilt filter missing ids or still dirty")
	}
}

// 重建期间有 ID 加入失败（脏标记被写入）：再重建一轮。
func TestBloomRebuildRepeatsWhenDirty(t *testing.T) {
	e := newBloomEnv(t, 1)
	var once sync.Once
	e.table.onScan = func() {
		once.Do(func() { e.rdb.Set(context.Background(), e.key+":dirty", 1, time.Minute) })
	}
	e.f.EnsureReady(context.Background())
	e.waitReady(t)
	if got := e.table.rounds.Load(); got != 2 {
		t.Fatalf("rebuild rounds = %d, want 2", got)
	}
}

// 每一轮都变脏：最多重建 3 轮，之后保持未就绪（查询放行），并释放锁。
func TestBloomStaysNotReadyWhenAlwaysDirty(t *testing.T) {
	e := newBloomEnv(t, 1)
	e.table.onScan = func() { e.rdb.Set(context.Background(), e.key+":dirty", 1, time.Minute) }
	e.f.EnsureReady(context.Background())
	deadline := time.Now().Add(5 * time.Second)
	for e.exists(t, e.key+":rebuild") || e.table.rounds.Load() < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("rebuild did not finish (rounds %d)", e.table.rounds.Load())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := e.table.rounds.Load(); got != 3 {
		t.Fatalf("rebuild rounds = %d, want 3", got)
	}
	if e.exists(t, e.key+":ready") {
		t.Fatal("dirty filter must not be marked ready")
	}
	if !e.f.MightExist(context.Background(), 404) {
		t.Fatal("not-ready filter must return true")
	}
}

// 别的实例持有重建锁：本实例不重建，也不动别人的锁。
func TestBloomRebuildSkippedWhenLocked(t *testing.T) {
	e := newBloomEnv(t, 1)
	ctx := context.Background()
	if err := e.rdb.Set(ctx, e.key+":rebuild", "other-instance", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	e.f.EnsureReady(ctx)
	time.Sleep(300 * time.Millisecond)
	if e.table.rounds.Load() != 0 || e.exists(t, e.key+":ready") {
		t.Fatal("rebuilt while another instance holds the lock")
	}
	if owner, _ := e.rdb.Get(ctx, e.key+":rebuild").Result(); owner != "other-instance" {
		t.Fatalf("lock owner = %q, want other-instance", owner)
	}
}

// Redis 不可用时放行。
func TestBloomRedisDownFailsOpen(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 100 * time.Millisecond, MaxRetries: -1})
	t.Cleanup(func() { _ = rdb.Close() })
	f := bloom.New(rdb, bloom.Options{
		Key: "e2e:bloom:down", Capacity: 1000, ErrorRate: 0.001,
		Scan: func(context.Context, int64, int) ([]int64, error) { return nil, nil },
	}, quiet)
	if !f.MightExist(context.Background(), 1) {
		t.Fatal("unavailable filter must return true")
	}
}
