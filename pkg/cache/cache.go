// Package cache 实现 Cache-Aside 读缓存，内置三类问题的防护：
//   - 穿透：回源结果为“不存在”时缓存空值标记（较短 TTL）；
//   - 击穿：进程内用 singleflight 合并同一个键的并发回源；
//   - 雪崩：TTL 加随机抖动；Redis 不可用时直接回源，不让缓存故障变成整体故障。
//
// 写操作由调用方先更新数据库，再调用 Delete 删除缓存。
package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"
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
)

// Options 是缓存参数。
type Options struct {
	TTL     time.Duration
	NullTTL time.Duration
}

// Cache 缓存类型为 T 的值，用 JSON 编码。
type Cache[T any] struct {
	rdb    redis.UniversalClient
	opts   Options
	sf     singleflight.Group
	logger *slog.Logger
}

// New 创建缓存。
func New[T any](rdb redis.UniversalClient, opts Options, logger *slog.Logger) *Cache[T] {
	return &Cache[T]{rdb: rdb, opts: opts, logger: logger}
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
				return zero, ErrNotFound
			}
			return v, nil
		}
		// 编码格式变化等导致的脏数据：当作未命中，回源后覆盖
		c.logger.Warn("cache decode failed", slog.String("key", key), slog.Any("error", decErr))
	case errors.Is(err, redis.Nil):
	default:
		// Redis 不可用：降级为直接回源，仍经过 singleflight 保护数据库，且不回填
		c.logger.Warn("cache get failed, falling back to loader", slog.String("key", key), slog.Any("error", err))
		return c.load(ctx, key, load, false)
	}
	return c.load(ctx, key, load, true)
}

func (c *Cache[T]) load(ctx context.Context, key string, load func(ctx context.Context) (T, error), fill bool) (T, error) {
	var zero T
	res, err, _ := c.sf.Do(key, func() (any, error) {
		lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), loadTimeout)
		defer cancel()
		v, err := load(lctx)
		if err != nil && !errors.Is(err, ErrNotFound) {
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
		c.logger.Warn("cache mget failed, falling back to loader", slog.Int("keys", len(keys)), slog.Any("error", err))
		fill = false
	} else {
		missing = missing[:0:0]
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
			}
		}
	}
	if len(missing) == 0 {
		return out, nil
	}

	loaded, err := loadMissing(ctx, missing)
	if err != nil {
		return nil, err
	}
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
	f := 1 + jitterRatio*(2*rand.Float64()-1)
	return time.Duration(float64(ttl) * f)
}
