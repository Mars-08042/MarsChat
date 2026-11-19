package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Mars-08042/MarsChat/pkg/conf"
)

// NewRedis 创建 Redis 客户端并 ping 一次，Redis 不可用时启动失败。
// 超时为 0 时使用 go-redis 的默认值。
func NewRedis(c *conf.Redis, logger *slog.Logger) (*redis.Client, func(), error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:         c.GetAddr(),
		Password:     c.GetPassword(),
		DB:           int(c.GetDb()),
		DialTimeout:  c.GetDialTimeout().AsDuration(),
		ReadTimeout:  c.GetReadTimeout().AsDuration(),
		WriteTimeout: c.GetWriteTimeout().AsDuration(),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		return nil, nil, fmt.Errorf("ping redis %s: %w", c.GetAddr(), err)
	}

	cleanup := func() {
		if err := rdb.Close(); err != nil {
			logger.Error("close redis failed", slog.Any("error", err))
		}
	}
	return rdb, cleanup, nil
}

// RedisCheck 返回检查 Redis 连通性的就绪检查项。
func RedisCheck(rdb *redis.Client) Check {
	return Check{Name: "redis", Fn: func(ctx context.Context) error {
		return rdb.Ping(ctx).Err()
	}}
}
