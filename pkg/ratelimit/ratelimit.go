// Package ratelimit 实现基于 Redis 的滑动窗口限流，多个实例共享同一个计数。
package ratelimit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// 滑动窗口日志：ZSET 中每个成员是一次请求，分数为请求时间（毫秒）。
// 时间取 Redis 服务器的 TIME，避免多个实例之间的时钟差影响计数。
// 返回 {是否放行, 被拒绝时还需等待的毫秒数}。
var slidingWindow = redis.NewScript(`
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local window = tonumber(ARGV[1])
local limit = tonumber(ARGV[2])
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now - window)
if redis.call('ZCARD', KEYS[1]) < limit then
  redis.call('ZADD', KEYS[1], now, ARGV[3])
  redis.call('PEXPIRE', KEYS[1], window)
  return {1, 0}
end
local oldest = redis.call('ZRANGE', KEYS[1], 0, 0, 'WITHSCORES')
local wait = tonumber(oldest[2]) + window - now
if wait < 1 then wait = 1 end
return {0, wait}`)

// Result 是一次限流判断的结果。
type Result struct {
	Allowed bool
	// RetryAfter 是被拒绝时距离窗口内最早一次请求过期的时间
	RetryAfter time.Duration
}

// Limiter 是滑动窗口限流器。
type Limiter struct {
	rdb redis.Scripter
}

// New 创建限流器。
func New(rdb redis.Scripter) *Limiter {
	return &Limiter{rdb: rdb}
}

// Allow 判断 key 在 window 内是否还能再请求一次（最多 limit 次）。放行时计入本次请求。
// Redis 出错时返回 error，由调用方决定放行还是拒绝。
func (l *Limiter) Allow(ctx context.Context, key string, limit int, window time.Duration) (Result, error) {
	member, err := randomMember()
	if err != nil {
		return Result{}, err
	}
	vals, err := slidingWindow.Run(ctx, l.rdb, []string{key}, window.Milliseconds(), limit, member).Int64Slice()
	if err != nil {
		return Result{}, fmt.Errorf("ratelimit %s: %w", key, err)
	}
	return Result{Allowed: vals[0] == 1, RetryAfter: time.Duration(vals[1]) * time.Millisecond}, nil
}

// 同一毫秒内可能有多次请求，成员需要唯一
func randomMember() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
