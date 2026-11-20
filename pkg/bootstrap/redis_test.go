package bootstrap

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/Mars-08042/MarsChat/pkg/conf"
)

var errDial = errors.New("lookup redis: no such host")

// slowDialer 模拟停掉的 Redis：down 时每次拨号要等 delay 才失败。
type slowDialer struct {
	down  atomic.Bool
	delay time.Duration
	calls atomic.Int32
}

func (d *slowDialer) dial(ctx context.Context, _, _ string) (net.Conn, error) {
	d.calls.Add(1)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if d.down.Load() {
		time.Sleep(d.delay)
		return nil, errDial
	}
	c, s := net.Pipe()
	_ = s.Close()
	return c, nil
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestDialGuardFailsFastUntilProbeSucceeds(t *testing.T) {
	d := &slowDialer{delay: 100 * time.Millisecond}
	d.down.Store(true)
	g := newDialGuard(time.Second, 20*time.Millisecond, discardLogger())
	t.Cleanup(g.close)
	dial := g.DialHook(d.dial)

	begin := time.Now()
	if _, err := dial(context.Background(), "tcp", "redis:6379"); !errors.Is(err, errDial) {
		t.Fatalf("first dial: got %v, want %v", err, errDial)
	}
	if took := time.Since(begin); took < d.delay {
		t.Fatalf("first dial took %v, want a real dial (>= %v)", took, d.delay)
	}

	// 不可达期间的拨号不再真的拨号，立即返回上次的错误
	for range 5 {
		begin = time.Now()
		_, err := dial(context.Background(), "tcp", "redis:6379")
		if !errors.Is(err, errDial) {
			t.Fatalf("dial while down: got %v, want %v", err, errDial)
		}
		if took := time.Since(begin); took > 50*time.Millisecond {
			t.Fatalf("dial while down took %v, want it to fail fast", took)
		}
	}

	d.down.Store(false)
	deadline := time.Now().Add(2 * time.Second)
	for {
		c, err := dial(context.Background(), "tcp", "redis:6379")
		if err == nil {
			_ = c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("dials still failing after Redis came back: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestDialGuardIgnoresCallerCancel(t *testing.T) {
	d := &slowDialer{}
	g := newDialGuard(time.Second, time.Hour, discardLogger())
	t.Cleanup(g.close)
	dial := g.DialHook(d.dial)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := dial(ctx, "tcp", "redis:6379"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled dial: got %v", err)
	}
	// 调用方取消不算 Redis 不可达，下一次拨号照常进行
	c, err := dial(context.Background(), "tcp", "redis:6379")
	if err != nil {
		t.Fatalf("dial after a canceled one: %v", err)
	}
	_ = c.Close()
	if n := d.calls.Load(); n != 2 {
		t.Fatalf("dialer called %d times, want 2", n)
	}
}

func TestNewRedisRecoversAfterRestart(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb, cleanup, err := NewRedis(&conf.Redis{Addr: mr.Addr()}, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	ctx := context.Background()

	mr.Close()
	for range 3 {
		if err := rdb.Ping(ctx).Err(); err == nil {
			t.Fatal("ping succeeded while Redis is stopped")
		}
	}

	if err := mr.Restart(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(redisProbeInterval + 2*time.Second)
	for {
		err := rdb.Ping(ctx).Err()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("ping still failing after Redis restarted: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
