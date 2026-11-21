// Package cache 实现 Cache-Aside 读缓存，内置三类问题的防护：
//   - 穿透：回源结果为“不存在”时缓存空值标记（较短 TTL）；
//   - 击穿：进程内用 singleflight 合并同一个键的并发回源；热点键可开启跨实例重建锁（Options.RebuildLock）；
//   - 雪崩：TTL 加随机抖动；Redis 不可用时直接回源，不让缓存故障变成整体故障。
//
// 写操作由调用方先更新数据库，再调用 Delete 删除缓存。
//
// 指标（标签 cache 为 Options.Name）：
//   - marschat_cache_requests_total{result}：hit 命中、null_hit 命中空值标记、miss 未命中、error Redis 不可用；
//   - marschat_cache_loads_total{result}：实际调用回源函数的次数，ok、not_found、error。
//     与 miss 对比可以看出 singleflight 和重建锁合并了多少回源；GetMany 一批算一次。
package cache

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	mrand "math/rand/v2"
	"time"

	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"golang.org/x/sync/singleflight"

	"github.com/Mars-08042/MarsChat/pkg/telemetry"
)

var (
	requestsCounter = telemetry.Int64Counter("marschat_cache_requests", "缓存读取次数，按结果区分")
	loadsCounter    = telemetry.Int64Counter("marschat_cache_loads", "缓存未命中后实际回源的次数")
)

// ErrNotFound 由回源函数返回，表示数据不存在；Cache 会为它写入空值标记。
var ErrNotFound = errors.New("cache: not found")

const (
	// 值的第一个字节区分空值标记和正常值
	markerNull  byte = 0
	markerValue byte = 1

	// 回源不受调用方取消影响（singleflight 中其他等待者依赖这次结果），但有独立超时
	loadTimeout = 3 * time.Second
	// TTL 随机抖动幅度，避免同一时间写入的键同时过期
	jitterRatio = 0.1

	// 重建锁的过期时间，覆盖一次回源的耗时（回源超时为 loadTimeout）
	lockTTL = 3 * time.Second
	// 没抢到重建锁时每隔 lockPollInterval 重读一次缓存，最多 lockPolls 次（共 200 毫秒）
	lockPollInterval = 50 * time.Millisecond
	lockPolls        = 4
)

// 只有持有者才能释放重建锁，防止回源超过锁的有效期后误删别人的锁
var releaseLockScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0`)

// Options 是缓存参数。
type Options struct {
	// Name 是缓存名，作为指标标签 cache，例如 account_user
	Name    string
	TTL     time.Duration
	NullTTL time.Duration
	// RebuildLock 开启跨实例重建锁：未命中时先抢 {key}:lock，抢到的实例回源并回填；
	// 没抢到的实例短暂轮询缓存，等不到再直接回源，宁可多一次查询也不让请求失败。
	// singleflight 只能合并本实例的请求，多实例同时读同一个热点键时才需要开启。
	RebuildLock bool
}

// Cache 缓存类型为 T 的值，用 JSON 编码。
type Cache[T any] struct {
	rdb    redis.UniversalClient
	opts   Options
	sf     singleflight.Group
	logger *slog.Logger
	m      cacheMetrics
}

// cacheMetrics 预先构造各结果的标签，记录时不再分配。
type cacheMetrics struct {
	hit, nullHit, miss, err       metric.AddOption
	loadOK, loadNotFound, loadErr metric.AddOption
}

func newCacheMetrics(name string) cacheMetrics {
	opt := func(result string) metric.AddOption {
		return metric.WithAttributeSet(attribute.NewSet(attribute.String("cache", name), attribute.String("result", result)))
	}
	return cacheMetrics{
		hit: opt("hit"), nullHit: opt("null_hit"), miss: opt("miss"), err: opt("error"),
		loadOK: opt("ok"), loadNotFound: opt("not_found"), loadErr: opt("error"),
	}
}

// New 创建缓存。
func New[T any](rdb redis.UniversalClient, opts Options, logger *slog.Logger) *Cache[T] {
	return &Cache[T]{rdb: rdb, opts: opts, logger: logger, m: newCacheMetrics(opts.Name)}
}

// Get 读取 key，未命中时调用 load 回源并回填。load 返回 ErrNotFound 时写入空值标记并返回 ErrNotFound。
func (c *Cache[T]) Get(ctx context.Context, key string, load func(ctx context.Context) (T, error)) (T, error) {
	var zero T
	data, err := c.rdb.Get(ctx, key).Bytes()
	switch {
	case err == nil:
		v, found, decErr := c.decode(data)
		if decErr == nil {
			if !found {
				requestsCounter.Add(ctx, 1, c.m.nullHit)
				return zero, ErrNotFound
			}
			requestsCounter.Add(ctx, 1, c.m.hit)
			return v, nil
		}
		// 编码格式变化等导致的脏数据：当作未命中，回源后覆盖
		c.logger.Warn("cache decode failed", slog.String("key", key), slog.Any("error", decErr))
	case errors.Is(err, redis.Nil):
	default:
		// Redis 不可用：降级为直接回源，仍经过 singleflight 保护数据库，且不回填
		requestsCounter.Add(ctx, 1, c.m.err)
		c.logger.Warn("cache get failed, falling back to loader", slog.String("key", key), slog.Any("error", err))
		return c.load(ctx, key, load, false)
	}
	requestsCounter.Add(ctx, 1, c.m.miss)
	return c.load(ctx, key, load, true)
}

func (c *Cache[T]) load(ctx context.Context, key string, load func(ctx context.Context) (T, error), fill bool) (T, error) {
	var zero T
	res, err, _ := c.sf.Do(key, func() (any, error) {
		lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), loadTimeout)
		defer cancel()
		if fill && c.opts.RebuildLock {
			release, err := c.tryLock(lctx, key)
			switch {
			case err != nil:
				c.logger.Warn("cache rebuild lock failed, loading directly", slog.String("key", key), slog.Any("error", err))
			case release != nil:
				// 回填之后才释放：等待中的实例在锁释放前就能读到新值
				defer release()
			default:
				if v, found, ok := c.waitFilled(lctx, key); ok {
					if !found {
						return nil, ErrNotFound
					}
					return v, nil
				}
			}
		}
		v, err := load(lctx)
		switch {
		case err == nil:
			loadsCounter.Add(lctx, 1, c.m.loadOK)
		case errors.Is(err, ErrNotFound):
			loadsCounter.Add(lctx, 1, c.m.loadNotFound)
		default:
			loadsCounter.Add(lctx, 1, c.m.loadErr)
			return nil, err
		}
		if fill {
			c.set(lctx, key, v, errors.Is(err, ErrNotFound))
		}
		if err != nil {
			return nil, err
		}
		return v, nil
	})
	if err != nil {
		return zero, err
	}
	return res.(T), nil
}

// tryLock 抢重建锁。抢到时返回释放函数；锁被别的实例持有时返回 nil, nil。
func (c *Cache[T]) tryLock(ctx context.Context, key string) (func(), error) {
	lockKey := key + ":lock"
	token := rand.Text()
	ok, err := c.rdb.SetNX(ctx, lockKey, token, lockTTL).Result()
	if err != nil || !ok {
		return nil, err
	}
	return func() {
		if err := releaseLockScript.Run(context.WithoutCancel(ctx), c.rdb, []string{lockKey}, token).Err(); err != nil {
			c.logger.Warn("cache rebuild lock release failed", slog.String("key", key), slog.Any("error", err))
		}
	}, nil
}

// waitFilled 等待持锁的实例回填，读到值（包括空值标记）时 ok 为 true。
func (c *Cache[T]) waitFilled(ctx context.Context, key string) (v T, found, ok bool) {
	ticker := time.NewTicker(lockPollInterval)
	defer ticker.Stop()
	for range lockPolls {
		select {
		case <-ctx.Done():
			return v, false, false
		case <-ticker.C:
		}
		data, err := c.rdb.Get(ctx, key).Bytes()
		if err != nil {
			continue
		}
		if v, found, err := c.decode(data); err == nil {
			return v, found, true
		}
	}
	return v, false, false
}

// GetMany 批量读取，返回命中或回源得到的值（不存在的键不在结果中）。
// loadMissing 收到未命中的键，返回其中存在的数据；没有返回的键写入空值标记。
// 批量回源不经过 singleflight：不同请求的键集合通常不同，合并收益很小。
func (c *Cache[T]) GetMany(ctx context.Context, keys []string, loadMissing func(ctx context.Context, missing []string) (map[string]T, error)) (map[string]T, error) {
	out := make(map[string]T, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	missing := keys
	fill := true
	vals, err := c.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		requestsCounter.Add(ctx, int64(len(keys)), c.m.err)
		c.logger.Warn("cache mget failed, falling back to loader", slog.Int("keys", len(keys)), slog.Any("error", err))
		fill = false
	} else {
		missing = missing[:0:0]
		var hits, nullHits int64
		for i, raw := range vals {
			s, ok := raw.(string)
			if !ok {
				missing = append(missing, keys[i])
				continue
			}
			v, found, decErr := c.decode([]byte(s))
			if decErr != nil {
				missing = append(missing, keys[i])
				continue
			}
			if found {
				out[keys[i]] = v
				hits++
			} else {
				nullHits++
			}
		}
		requestsCounter.Add(ctx, hits, c.m.hit)
		requestsCounter.Add(ctx, nullHits, c.m.nullHit)
		requestsCounter.Add(ctx, int64(len(missing)), c.m.miss)
	}
	if len(missing) == 0 {
		return out, nil
	}

	loaded, err := loadMissing(ctx, missing)
	if err != nil {
		loadsCounter.Add(ctx, 1, c.m.loadErr)
		return nil, err
	}
	loadsCounter.Add(ctx, 1, c.m.loadOK)
	if fill {
		pipe := c.rdb.Pipeline()
		for _, k := range missing {
			v, found := loaded[k]
			c.pipeSet(ctx, pipe, k, v, !found)
		}
		if _, err := pipe.Exec(ctx); err != nil {
			c.logger.Warn("cache fill failed", slog.Int("keys", len(missing)), slog.Any("error", err))
		}
	}
	for k, v := range loaded {
		out[k] = v
	}
	return out, nil
}

// Delete 删除缓存。调用方在数据库提交之后调用；失败时由调用方决定是否重试。
func (c *Cache[T]) Delete(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	if err := c.rdb.Del(ctx, keys...).Err(); err != nil {
		return fmt.Errorf("cache delete: %w", err)
	}
	return nil
}

func (c *Cache[T]) set(ctx context.Context, key string, v T, null bool) {
	data, ttl, err := c.encode(v, null)
	if err != nil {
		c.logger.Warn("cache encode failed", slog.String("key", key), slog.Any("error", err))
		return
	}
	if err := c.rdb.Set(ctx, key, data, ttl).Err(); err != nil {
		c.logger.Warn("cache set failed", slog.String("key", key), slog.Any("error", err))
	}
}

func (c *Cache[T]) pipeSet(ctx context.Context, pipe redis.Pipeliner, key string, v T, null bool) {
	data, ttl, err := c.encode(v, null)
	if err != nil {
		c.logger.Warn("cache encode failed", slog.String("key", key), slog.Any("error", err))
		return
	}
	pipe.Set(ctx, key, data, ttl)
}

func (c *Cache[T]) encode(v T, null bool) ([]byte, time.Duration, error) {
	if null {
		return []byte{markerNull}, c.opts.NullTTL, nil
	}
	body, err := json.Marshal(v)
	if err != nil {
		return nil, 0, err
	}
	return append([]byte{markerValue}, body...), withJitter(c.opts.TTL), nil
}

func (c *Cache[T]) decode(data []byte) (v T, found bool, err error) {
	if len(data) == 0 {
		return v, false, errors.New("empty cache value")
	}
	switch data[0] {
	case markerNull:
		return v, false, nil
	case markerValue:
		if err := json.Unmarshal(data[1:], &v); err != nil {
			return v, false, err
		}
		return v, true, nil
	default:
		return v, false, fmt.Errorf("unknown cache marker %d", data[0])
	}
}

// withJitter 返回 ttl × [1-jitterRatio, 1+jitterRatio) 之间的随机值。
func withJitter(ttl time.Duration) time.Duration {
	f := 1 + jitterRatio*(2*mrand.Float64()-1)
	return time.Duration(float64(ttl) * f)
}
