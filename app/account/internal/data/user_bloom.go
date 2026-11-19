package data

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// 用户 ID 布隆过滤器（RedisBloom），拦截不存在的 ID，防止缓存穿透。
//
// 过滤器漏加会把真实用户判为不存在，所以只在就绪标记和过滤器都存在时才信任“不存在”：
//   - 注册提交后加入过滤器，失败时后台重试；重试仍失败则写入脏标记、删除就绪标记并重建；
//   - 重建先清空过滤器再扫描全表，扫描期间并发注册的 ID 照常加入，不会丢失；
//   - 就绪标记带 TTL，到期后重建一次，兜底 Redis 故障期间没能记下的漏加。
const (
	bloomKey      = "account:user_bf"
	bloomReadyKey = "account:user_bf:ready"
	bloomDirtyKey = "account:user_bf:dirty"
	bloomLockKey  = "account:user_bf:rebuild"

	bloomCapacity  = 1_000_000
	bloomErrorRate = 0.001
	bloomBatch     = 1000

	bloomReadyTTL       = 24 * time.Hour
	bloomLockTTL        = 5 * time.Minute
	bloomRebuildBackoff = time.Minute
	bloomMaxRounds      = 3
)

// 只有持有者才能释放重建锁，防止锁过期后误删别人的锁
var releaseLockScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0`)

type userBloom struct {
	d      *Data
	logger *slog.Logger

	rebuilding atomic.Bool
	mu         sync.Mutex
	lastTry    time.Time
	// addRetry 是注册后加入过滤器失败时的重试间隔，测试中可以调小
	addRetry []time.Duration
}

func newUserBloom(d *Data, logger *slog.Logger) *userBloom {
	return &userBloom{
		d: d, logger: logger.With(slog.String("component", "user_bloom")),
		addRetry: []time.Duration{time.Second, 2 * time.Second, 4 * time.Second},
	}
}

// ensureReady 在启动时检查过滤器，未就绪时后台重建。
func (b *userBloom) ensureReady(ctx context.Context) {
	n, err := b.d.rdb.Exists(ctx, bloomReadyKey, bloomKey).Result()
	if err != nil {
		b.logger.Warn("check bloom filter failed", slog.Any("error", err))
		return
	}
	if n != 2 {
		b.rebuildAsync()
	}
}

// mightExist 返回 false 表示 ID 一定不存在；过滤器不可用或未就绪时返回 true，由调用方查库。
func (b *userBloom) mightExist(ctx context.Context, id int64) bool {
	pipe := b.d.rdb.Pipeline()
	ready := pipe.Exists(ctx, bloomReadyKey)
	filter := pipe.Exists(ctx, bloomKey)
	has := pipe.Do(ctx, "BF.EXISTS", bloomKey, id)
	if _, err := pipe.Exec(ctx); err != nil {
		b.logger.Debug("bloom check failed", slog.Any("error", err))
		return true
	}
	if ready.Val() == 0 || filter.Val() == 0 {
		// 标记过期，或过滤器被淘汰而标记还在
		b.rebuildAsync()
		return true
	}
	v, err := has.Int64()
	return err != nil || v == 1
}

// add 把新注册的 ID 加入过滤器。
func (b *userBloom) add(ctx context.Context, id int64) {
	if err := b.insert(ctx, id); err == nil {
		return
	}
	go func() {
		for _, d := range b.addRetry {
			time.Sleep(d)
			if err := b.insert(ctx, id); err == nil {
				return
			}
		}
		b.logger.Error("add user to bloom filter failed, rebuilding", slog.Int64("user_id", id))
		// 先写脏标记：如果别的实例正在重建，它在结束时看到标记会再重建一轮
		pipe := b.d.rdb.TxPipeline()
		pipe.Set(ctx, bloomDirtyKey, 1, bloomLockTTL*2)
		pipe.Del(ctx, bloomReadyKey)
		if _, err := pipe.Exec(ctx); err != nil {
			b.logger.Error("mark bloom filter dirty failed", slog.Any("error", err))
		}
		b.rebuildAsync()
	}()
}

func (b *userBloom) insert(ctx context.Context, ids ...int64) error {
	args := make([]any, 0, 7+len(ids))
	args = append(args, "BF.INSERT", bloomKey, "CAPACITY", bloomCapacity, "ERROR", bloomErrorRate, "ITEMS")
	for _, id := range ids {
		args = append(args, id)
	}
	return b.d.rdb.Do(ctx, args...).Err()
}

// rebuildAsync 在后台重建。进程内同时只跑一个，失败后一分钟内不再尝试。
func (b *userBloom) rebuildAsync() {
	b.mu.Lock()
	if time.Since(b.lastTry) < bloomRebuildBackoff {
		b.mu.Unlock()
		return
	}
	b.lastTry = time.Now()
	b.mu.Unlock()
	if !b.rebuilding.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer b.rebuilding.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), bloomLockTTL)
		defer cancel()
		if err := b.rebuild(ctx); err != nil {
			b.logger.Error("rebuild bloom filter failed", slog.Any("error", err))
		}
	}()
}

// rebuild 用分布式锁保证多个实例中只有一个在重建。
func (b *userBloom) rebuild(ctx context.Context) error {
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return err
	}
	owner := hex.EncodeToString(token)
	ok, err := b.d.rdb.SetNX(ctx, bloomLockKey, owner, bloomLockTTL).Result()
	if err != nil || !ok {
		return err
	}
	defer func() {
		if err := releaseLockScript.Run(context.WithoutCancel(ctx), b.d.rdb, []string{bloomLockKey}, owner).Err(); err != nil {
			b.logger.Warn("release bloom rebuild lock failed", slog.Any("error", err))
		}
	}()

	for round := 1; round <= bloomMaxRounds; round++ {
		start := time.Now()
		n, err := b.rebuildOnce(ctx)
		if err != nil {
			return err
		}
		// 重建期间有注册加入失败，本轮结果可能漏掉它，再来一轮
		dirty, err := b.d.rdb.Exists(ctx, bloomDirtyKey).Result()
		if err != nil {
			return err
		}
		if dirty == 0 {
			if err := b.d.rdb.Set(ctx, bloomReadyKey, 1, bloomReadyTTL).Err(); err != nil {
				return err
			}
			b.logger.Info("bloom filter rebuilt", slog.Int("users", n), slog.Duration("elapsed", time.Since(start)))
			return nil
		}
	}
	b.logger.Warn("bloom filter still dirty after rebuild, leaving it not ready")
	return nil
}

func (b *userBloom) rebuildOnce(ctx context.Context) (int, error) {
	// 清空和创建放在一个事务里，避免并发注册的 BF.INSERT 在两者之间用默认参数建出过滤器
	pipe := b.d.rdb.TxPipeline()
	pipe.Del(ctx, bloomReadyKey, bloomDirtyKey, bloomKey)
	pipe.Do(ctx, "BF.RESERVE", bloomKey, bloomErrorRate, bloomCapacity)
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, err
	}
	var (
		lastID int64
		total  int
	)
	for {
		var ids []int64
		err := b.d.db.WithContext(ctx).Model(&userPO{}).
			Where("id > ?", lastID).Order("id").Limit(bloomBatch).Pluck("id", &ids).Error
		if err != nil {
			return total, err
		}
		if len(ids) == 0 {
			return total, nil
		}
		if err := b.insert(ctx, ids...); err != nil {
			return total, err
		}
		total += len(ids)
		lastID = ids[len(ids)-1]
	}
}
