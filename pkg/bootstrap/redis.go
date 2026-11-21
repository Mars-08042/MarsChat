package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Mars-08042/MarsChat/pkg/conf"
)

// redisProbeInterval 是 Redis 不可达期间后台探测的间隔，Redis 恢复后最多晚这么久恢复拨号。
const redisProbeInterval = time.Second

// NewRedis 创建 Redis 客户端并 ping 一次，Redis 不可用时启动失败。
// 超时为 0 时使用 go-redis 的默认值。
//
// 客户端带拨号保护（见 dialGuard），单次拨号不再重试：Redis 不可达时命令立即失败，
// 调用方按各自的降级策略处理（限流放行、缓存回源等），不会被拨号拖到请求超时。
func NewRedis(c *conf.Redis, logger *slog.Logger) (*redis.Client, func(), error) {
	// go-redis 的内部日志是进程级全局设置；每个进程只创建一个客户端，在这里设置即可
	redis.SetLogger(redisLogger{logger: logger})
	opts := &redis.Options{
		Addr:         c.GetAddr(),
		Password:     c.GetPassword(),
		DB:           int(c.GetDb()),
		DialTimeout:  c.GetDialTimeout().AsDuration(),
		ReadTimeout:  c.GetReadTimeout().AsDuration(),
		WriteTimeout: c.GetWriteTimeout().AsDuration(),
		// 默认 5 次，每次间隔 100 毫秒；拨号保护生效后重试只会白白等待，连接失败交给命令级重试
		DialerRetries: 1,
	}
	rdb := redis.NewClient(opts)
	timeout := opts.DialTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	guard := newDialGuard(timeout, redisProbeInterval, logger)
	rdb.AddHook(guard)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		guard.close()
		_ = rdb.Close()
		return nil, nil, fmt.Errorf("ping redis %s: %w", c.GetAddr(), err)
	}

	cleanup := func() {
		guard.close()
		if err := rdb.Close(); err != nil {
			logger.Error("close redis failed", slog.Any("error", err))
		}
	}
	return rdb, cleanup, nil
}

// redisLogger 把 go-redis 的内部日志转到 slog（默认用标准库 log 写纯文本到 stderr）。
// go-redis 只给出格式化后的文本、不带级别，这里按内容分级：
//   - 连接池拨号失败：Redis 不可达期间每次拨号都打一行，拨号保护已在故障开始和恢复时各记一条，降为 DEBUG；
//   - 内部恢复的 panic：记 ERROR；
//   - 其他（关闭连接失败、读取推送消息失败等）：不直接影响请求结果，记 WARN。
type redisLogger struct {
	logger *slog.Logger
}

func (l redisLogger) Printf(ctx context.Context, format string, v ...any) {
	msg := fmt.Sprintf(format, v...)
	level := slog.LevelWarn
	switch {
	case strings.HasPrefix(format, "redis: connection pool: failed to dial"):
		level = slog.LevelDebug
	case strings.Contains(msg, "panic"):
		level = slog.LevelError
	}
	l.logger.Log(ctx, level, "go-redis internal log", slog.String("detail", strings.TrimPrefix(msg, "redis: ")))
}

// RedisCheck 返回检查 Redis 连通性的就绪检查项。
func RedisCheck(rdb *redis.Client) Check {
	return Check{Name: "redis", Fn: func(ctx context.Context) error {
		return rdb.Ping(ctx).Err()
	}}
}

// dialGuard 在拨号失败后把 Redis 标记为不可达：期间的拨号直接返回上次的错误，
// 由一个后台协程立即探测、之后每隔 interval 探测一次，探测成功后恢复正常拨号。
//
// 不加保护时，每条命令都要真的拨号才知道失败。Compose 里停掉的容器会从 Docker DNS 中消失，
// 解析服务名要转发到上游 DNS，约 1 秒才返回 no such host；加上 go-redis 的拨号重试和命令重试，
// 一条命令要耗几秒，用完调用方的截止时间，本该放行的限流和吊销检查变成请求超时。
//
// 误判的代价很小：调用方截止时间太短导致的拨号超时也会触发标记，但后台探测立即开始，
// Redis 正常时几毫秒内就会恢复。调用方主动取消导致的失败不触发标记。
type dialGuard struct {
	timeout  time.Duration
	interval time.Duration
	logger   *slog.Logger
	stop     chan struct{}
	once     sync.Once

	mu      sync.Mutex
	err     error // 非 nil 表示不可达，期间的拨号直接返回它
	since   time.Time
	probing bool
}

var _ redis.Hook = (*dialGuard)(nil)

func newDialGuard(timeout, interval time.Duration, logger *slog.Logger) *dialGuard {
	return &dialGuard{timeout: timeout, interval: interval, logger: logger, stop: make(chan struct{})}
}

func (g *dialGuard) DialHook(next redis.DialHook) redis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if err := g.unreachable(); err != nil {
			return nil, err
		}
		conn, err := next(ctx, network, addr)
		if err != nil && !errors.Is(ctx.Err(), context.Canceled) {
			g.markDown(err, func(ctx context.Context) error {
				c, err := next(ctx, network, addr)
				if err == nil {
					_ = c.Close()
				}
				return err
			})
		}
		return conn, err
	}
}

func (g *dialGuard) ProcessHook(next redis.ProcessHook) redis.ProcessHook { return next }

func (g *dialGuard) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func (g *dialGuard) unreachable() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.err
}

// markDown 记录不可达并启动探测协程；已经在探测时只更新错误。
func (g *dialGuard) markDown(err error, probe func(context.Context) error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.err == nil {
		g.since = time.Now()
		g.logger.Warn("redis unreachable, failing dials fast until a probe succeeds", slog.Any("error", err))
	}
	g.err = fmt.Errorf("redis unreachable: %w", err)
	if g.probing {
		return
	}
	g.probing = true
	go g.probeLoop(probe)
}

func (g *dialGuard) probeLoop(probe func(context.Context) error) {
	for {
		ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
		err := probe(ctx)
		cancel()

		g.mu.Lock()
		if err == nil {
			g.logger.Info("redis reachable again", slog.Duration("unreachable_for", time.Since(g.since)))
			g.err, g.probing = nil, false
			g.mu.Unlock()
			return
		}
		g.err = fmt.Errorf("redis unreachable: %w", err)
		g.mu.Unlock()

		select {
		case <-g.stop:
			g.mu.Lock()
			g.probing = false
			g.mu.Unlock()
			return
		case <-time.After(g.interval):
		}
	}
}

func (g *dialGuard) close() {
	g.once.Do(func() { close(g.stop) })
}
