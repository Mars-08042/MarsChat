// Package bloom 是基于 RedisBloom 的 ID 布隆过滤器，拦截不存在的 ID，防止缓存穿透。
//
// 过滤器漏加会把存在的 ID 判为不存在，所以只在就绪标记和过滤器都存在时才信任“不存在”：
//   - 新 ID 在数据库提交后加入过滤器，失败时后台重试；重试仍失败则写入脏标记、删除就绪标记并重建；
//   - 重建先清空过滤器再扫描全表，扫描期间并发加入的 ID 照常写入，不会丢失；
//   - 就绪标记带 TTL，到期后重建一次，兜底 Redis 故障期间没能记下的漏加；
//   - 多个实例用 Redis 锁保证同一时间只有一个在重建。
//
// 指标 marschat_bloom_checks_total{filter, result}：filter 为过滤器键名；result 为 reject（一定不存在，
// 拦截了一次查库）、pass（可能存在）、bypass（过滤器不可用或未就绪，按可能存在放行）。
package bloom

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/Mars-08042/MarsChat/pkg/telemetry"
)

const (
	readyTTL  = 24 * time.Hour
	lockTTL   = 5 * time.Minute
	maxRounds = 3
	scanBatch = 1000

	defaultRebuildBackoff = time.Minute
)

var defaultAddRetry = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}

// 只有持有者才能释放重建锁，防止锁过期后误删别人的锁
var releaseLockScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0`)

var checksCounter = telemetry.Int64Counter("marschat_bloom_checks", "布隆过滤器判断次数，按结果区分")

// Options 是过滤器参数。
type Options struct {
	// Key 是过滤器的键名；就绪标记、脏标记、重建锁的键名分别加后缀 :ready、:dirty、:rebuild
	Key       string
	Capacity  int64
	ErrorRate float64
	// Scan 按 ID 递增返回大于 after 的最多 limit 个 ID，返回空切片表示扫描结束
	Scan func(ctx context.Context, after int64, limit int) ([]int64, error)

	// AddRetry 是加入失败后的重试间隔，为空时取 1s、2s、4s；测试中可以调小
	AddRetry []time.Duration
	// RebuildBackoff 是本进程两次重建尝试的最小间隔，为零时取 1 分钟；测试中可以调小
	RebuildBackoff time.Duration
}

// Filter 是一个 ID 布隆过滤器。
type Filter struct {
	rdb      redis.UniversalClient
	opts     Options
	readyKey string
	dirtyKey string
	lockKey  string
	logger   *slog.Logger

	rebuilding atomic.Bool
	mu         sync.Mutex
	lastTry    time.Time

	// 预先构造的指标标签
	mPass, mReject, mBypass metric.AddOption
}

// New 创建过滤器，不访问 Redis。启动时调用 EnsureReady 检查是否需要重建。
func New(rdb redis.UniversalClient, opts Options, logger *slog.Logger) *Filter {
	if len(opts.AddRetry) == 0 {
		opts.AddRetry = defaultAddRetry
	}
	if opts.RebuildBackoff <= 0 {
		opts.RebuildBackoff = defaultRebuildBackoff
	}
	opt := func(result string) metric.AddOption {
		return metric.WithAttributeSet(attribute.NewSet(attribute.String("filter", opts.Key), attribute.String("result", result)))
	}
	return &Filter{
		rdb:      rdb,
		opts:     opts,
		readyKey: opts.Key + ":ready",
		dirtyKey: opts.Key + ":dirty",
		lockKey:  opts.Key + ":rebuild",
		logger:   logger.With(slog.String("component", "bloom"), slog.String("bloom_key", opts.Key)),
		mPass:    opt("pass"),
		mReject:  opt("reject"),
		mBypass:  opt("bypass"),
	}
}

// EnsureReady 检查过滤器，未就绪时后台重建。
func (f *Filter) EnsureReady(ctx context.Context) {
	n, err := f.rdb.Exists(ctx, f.readyKey, f.opts.Key).Result()
	if err != nil {
		f.logger.Warn("check bloom filter failed", slog.Any("error", err))
		return
	}
	if n != 2 {
		f.rebuildAsync()
	}
}

// MightExist 返回 false 表示 ID 一定不存在；过滤器不可用或未就绪时返回 true，由调用方查库。
func (f *Filter) MightExist(ctx context.Context, id int64) bool {
	pipe := f.rdb.Pipeline()
	ready := pipe.Exists(ctx, f.readyKey)
	filter := pipe.Exists(ctx, f.opts.Key)
	has := pipe.Do(ctx, "BF.EXISTS", f.opts.Key, id)
	if _, err := pipe.Exec(ctx); err != nil {
		f.logger.Debug("bloom check failed", slog.Any("error", err))
		checksCounter.Add(ctx, 1, f.mBypass)
		return true
	}
	if ready.Val() == 0 || filter.Val() == 0 {
		// 标记过期，或过滤器被淘汰而标记还在
		f.rebuildAsync()
		checksCounter.Add(ctx, 1, f.mBypass)
		return true
	}
	// BF.EXISTS 在 RESP2 下返回整数、在 RESP3（go-redis v9 默认）下返回布尔值，Bool() 两种都能解析。
	// 解析失败按“可能存在”放行，但要记 WARN：静默放行会让过滤器失效而无人察觉
	v, err := has.Bool()
	if err != nil {
		f.logger.Warn("decode BF.EXISTS reply failed", slog.Any("error", err))
		checksCounter.Add(ctx, 1, f.mBypass)
		return true
	}
	if v {
		checksCounter.Add(ctx, 1, f.mPass)
	} else {
		checksCounter.Add(ctx, 1, f.mReject)
	}
	return v
}

// Add 把新 ID 加入过滤器，在数据库提交后调用。失败时后台重试，不阻塞调用方。
func (f *Filter) Add(ctx context.Context, id int64) {
	ctx = context.WithoutCancel(ctx)
	if err := f.insert(ctx, id); err == nil {
		return
	}
	go func() {
		for _, d := range f.opts.AddRetry {
			time.Sleep(d)
			if err := f.insert(ctx, id); err == nil {
				return
			}
		}
		f.logger.Error("add id to bloom filter failed, rebuilding", slog.Int64("id", id))
		// 先写脏标记：如果别的实例正在重建，它在结束时看到标记会再重建一轮
		pipe := f.rdb.TxPipeline()
		pipe.Set(ctx, f.dirtyKey, 1, lockTTL*2)
		pipe.Del(ctx, f.readyKey)
		if _, err := pipe.Exec(ctx); err != nil {
			f.logger.Error("mark bloom filter dirty failed", slog.Any("error", err))
		}
		f.rebuildAsync()
	}()
}

func (f *Filter) insert(ctx context.Context, ids ...int64) error {
	args := make([]any, 0, 7+len(ids))
	args = append(args, "BF.INSERT", f.opts.Key, "CAPACITY", f.opts.Capacity, "ERROR", f.opts.ErrorRate, "ITEMS")
	for _, id := range ids {
		args = append(args, id)
	}
	return f.rdb.Do(ctx, args...).Err()
}

// rebuildAsync 在后台重建。进程内同时只跑一个，两次尝试至少间隔 RebuildBackoff。
func (f *Filter) rebuildAsync() {
	f.mu.Lock()
	if time.Since(f.lastTry) < f.opts.RebuildBackoff {
		f.mu.Unlock()
		return
	}
	f.lastTry = time.Now()
	f.mu.Unlock()
	if !f.rebuilding.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer f.rebuilding.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), lockTTL)
		defer cancel()
		if err := f.rebuild(ctx); err != nil {
			f.logger.Error("rebuild bloom filter failed", slog.Any("error", err))
		}
	}()
}

// rebuild 用分布式锁保证多个实例中只有一个在重建，没抢到锁时直接返回。
func (f *Filter) rebuild(ctx context.Context) error {
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return err
	}
	owner := hex.EncodeToString(token)
	ok, err := f.rdb.SetNX(ctx, f.lockKey, owner, lockTTL).Result()
	if err != nil || !ok {
		return err
	}
	defer func() {
		if err := releaseLockScript.Run(context.WithoutCancel(ctx), f.rdb, []string{f.lockKey}, owner).Err(); err != nil {
			f.logger.Warn("release bloom rebuild lock failed", slog.Any("error", err))
		}
	}()

	for round := 1; round <= maxRounds; round++ {
		start := time.Now()
		n, err := f.rebuildOnce(ctx)
		if err != nil {
			return err
		}
		// 重建期间有 ID 加入失败，本轮结果可能漏掉它，再来一轮
		dirty, err := f.rdb.Exists(ctx, f.dirtyKey).Result()
		if err != nil {
			return err
		}
		if dirty == 0 {
			if err := f.rdb.Set(ctx, f.readyKey, 1, readyTTL).Err(); err != nil {
				return err
			}
			f.logger.Info("bloom filter rebuilt", slog.Int("ids", n), slog.Int("round", round),
				slog.Duration("elapsed", time.Since(start)))
			return nil
		}
	}
	f.logger.Warn("bloom filter still dirty after rebuild, leaving it not ready")
	return nil
}

func (f *Filter) rebuildOnce(ctx context.Context) (int, error) {
	// 清空和创建放在一个事务里，避免并发的 BF.INSERT 在两者之间用默认参数建出过滤器
	pipe := f.rdb.TxPipeline()
	pipe.Del(ctx, f.readyKey, f.dirtyKey, f.opts.Key)
	pipe.Do(ctx, "BF.RESERVE", f.opts.Key, f.opts.ErrorRate, f.opts.Capacity)
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, err
	}
	var (
		lastID int64
		total  int
	)
	for {
		ids, err := f.opts.Scan(ctx, lastID, scanBatch)
		if err != nil {
			return total, err
		}
		if len(ids) == 0 {
			return total, nil
		}
		if err := f.insert(ctx, ids...); err != nil {
			return total, err
		}
		total += len(ids)
		lastID = ids[len(ids)-1]
	}
}
