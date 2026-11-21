package outbox

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/attribute"

	"github.com/Mars-08042/MarsChat/pkg/telemetry/telemetrytest"
)

// 发布失败按批次中的事件数计入 error，成功后计入 ok，延迟按事件写入时间计算。
func TestPublishMetrics(t *testing.T) {
	telemetrytest.Reader(t)
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	st, pr := &fakeStore{}, &fakeProducer{}
	opts := Options{Source: "metrics_test", Owner: "me", PollInterval: time.Hour, BatchSize: 10, Retention: time.Hour}
	pub := newPublisher(st, pr, rdb, opts, slog.New(slog.NewTextHandler(io.Discard, nil)))
	clock := time.Now()
	pub.now = func() time.Time { return clock }

	st.add(3)
	st.mu.Lock()
	for i := range st.events {
		st.events[i].CreatedAt = clock.Add(-2 * time.Second)
	}
	st.mu.Unlock()

	ctx := context.Background()
	pub.maintainLease(ctx)
	pr.err = errors.New("broker down")
	pub.drain(ctx)
	source := attribute.String("source", "metrics_test")
	if got := telemetrytest.Sum(t, "marschat_outbox_published", source, attribute.String("result", "error")); got != 3 {
		t.Errorf("error = %d, want 3", got)
	}

	pr.err = nil
	clock = clock.Add(minBackoff)
	pub.drain(ctx)
	if got := telemetrytest.Sum(t, "marschat_outbox_published", source, attribute.String("result", "ok")); got != 3 {
		t.Errorf("ok = %d, want 3", got)
	}
	if got := telemetrytest.HistogramCount(t, "marschat_outbox_publish_delay_seconds", source); got != 3 {
		t.Errorf("delay samples = %d, want 3", got)
	}
}
