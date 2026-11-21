package data

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Mars-08042/MarsChat/app/account/internal/biz"
)

// recordFailureScript 累计密码错误次数：第一次失败时开始统计窗口，达到上限时把 TTL 改为锁定时长。
var recordFailureScript = redis.NewScript(`
local n = redis.call('INCR', KEYS[1])
if n == 1 then redis.call('PEXPIRE', KEYS[1], ARGV[2]) end
if n == tonumber(ARGV[1]) then redis.call('PEXPIRE', KEYS[1], ARGV[3]) end
return n`)

type loginGuard struct{ rdb *redis.Client }

// NewLoginGuard 创建密码错误计数器。
func NewLoginGuard(rdb *redis.Client) biz.LoginGuard {
	return &loginGuard{rdb: rdb}
}

func loginFailKey(email string) string { return "account:login_fail:" + email }

func (g *loginGuard) Locked(ctx context.Context, email string, maxFailures int) (bool, time.Duration, error) {
	key := loginFailKey(email)
	pipe := g.rdb.Pipeline()
	get := pipe.Get(ctx, key)
	ttl := pipe.PTTL(ctx, key)
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return false, 0, err
	}
	n, err := get.Int()
	if err == redis.Nil {
		return false, 0, nil
	}
	if err != nil {
		return false, 0, err
	}
	if n < maxFailures {
		return false, 0, nil
	}
	return true, ttl.Val(), nil
}

func (g *loginGuard) RecordFailure(ctx context.Context, email string, maxFailures int, window, lock time.Duration) error {
	return recordFailureScript.Run(ctx, g.rdb, []string{loginFailKey(email)},
		maxFailures, window.Milliseconds(), lock.Milliseconds()).Err()
}

func (g *loginGuard) Reset(ctx context.Context, email string) error {
	return g.rdb.Del(ctx, loginFailKey(email)).Err()
}
