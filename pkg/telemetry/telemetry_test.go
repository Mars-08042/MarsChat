package telemetry

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	kerrors "github.com/go-kratos/kratos/v3/errors"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/proto"

	"github.com/Mars-08042/MarsChat/pkg/conf"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// 不配置 OTLP 地址时只启用指标：服务端中间件记录的请求出现在注册表中，名字带 Prometheus 风格的后缀。
func TestSetupExportsRequestMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	shutdown, err := setup(&conf.Telemetry{}, Info{Name: "test", Version: "v0", InstanceID: "test-1"}, reg, discardLogger())
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	defer shutdown()

	mws, err := ServerMiddlewares()
	if err != nil {
		t.Fatalf("ServerMiddlewares: %v", err)
	}
	if len(mws) != 2 {
		t.Fatalf("ServerMiddlewares returned %d middlewares, want 2", len(mws))
	}
	h := mws[1](func(context.Context, any) (any, error) {
		return nil, kerrors.ServiceUnavailable("DEPENDENCY_UNAVAILABLE", "redis down")
	})
	if _, err := h(context.Background(), nil); err == nil {
		t.Fatal("handler error was swallowed")
	}

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	counter := find(mfs, "server_requests_code_total")
	if counter == nil {
		t.Fatalf("server_requests_code_total not exported; got %v", names(mfs))
	}
	m := counter.GetMetric()[0]
	if label(m, "code") != "503" || label(m, "reason") != "DEPENDENCY_UNAVAILABLE" || m.GetCounter().GetValue() != 1 {
		t.Errorf("unexpected counter sample: %v", m)
	}
	hist := find(mfs, "server_requests_seconds")
	if hist == nil {
		t.Fatalf("server_requests_seconds not exported; got %v", names(mfs))
	}
	if got := len(hist.GetMetric()[0].GetHistogram().GetBucket()); got != len(LatencyBuckets) {
		t.Errorf("histogram has %d buckets, want %d", got, len(LatencyBuckets))
	}
	// 没有 otel_scope_* 标签
	for _, l := range m.GetLabel() {
		if strings.HasPrefix(l.GetName(), "otel_scope") {
			t.Errorf("unexpected scope label %s", l.GetName())
		}
	}
}

func TestSampleRatio(t *testing.T) {
	if got := sampleRatio(nil); got != 1 {
		t.Errorf("nil config: ratio = %v, want 1", got)
	}
	if got := sampleRatio(&conf.Telemetry{}); got != 1 {
		t.Errorf("unset: ratio = %v, want 1", got)
	}
	if got := sampleRatio(&conf.Telemetry{SampleRatio: proto.Float64(0)}); got != 0 {
		t.Errorf("explicit 0: ratio = %v, want 0", got)
	}
}

// 比例为 0 时不采样新 Trace，但上游已采样的请求照常采样，Trace 不会断成半条。
func TestSamplerFollowsParent(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp), sdktrace.WithSampler(newSampler(0)))
	defer func() { _ = tp.Shutdown(context.Background()) }()
	tracer := tp.Tracer("test")

	_, root := tracer.Start(context.Background(), "root")
	root.End()
	if root.SpanContext().IsSampled() {
		t.Error("root span sampled with ratio 0")
	}

	parent := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{1},
		SpanID:     trace.SpanID{1},
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	})
	_, child := tracer.Start(trace.ContextWithRemoteSpanContext(context.Background(), parent), "child")
	child.End()
	if !child.SpanContext().IsSampled() {
		t.Error("child of a sampled remote parent was not sampled")
	}
	if got := len(exp.GetSpans()); got != 1 {
		t.Errorf("exported %d spans, want 1", got)
	}
}

// 同一窗口内的错误只写一条日志，下一条带上被省略的条数。
func TestRateLimitedHandler(t *testing.T) {
	var buf bytes.Buffer
	h := newRateLimitedHandler(slog.New(slog.NewTextHandler(&buf, nil)), time.Hour)
	for range 5 {
		h.Handle(errors.New("export failed"))
	}
	if got := strings.Count(buf.String(), "telemetry error"); got != 1 {
		t.Fatalf("logged %d times within the window, want 1:\n%s", got, buf.String())
	}
	h.mu.Lock()
	h.last = time.Now().Add(-2 * time.Hour)
	h.mu.Unlock()
	h.Handle(errors.New("export failed"))
	if !strings.Contains(buf.String(), "suppressed=4") {
		t.Errorf("second log line does not report suppressed=4:\n%s", buf.String())
	}
}

func find(mfs []*dto.MetricFamily, name string) *dto.MetricFamily {
	for _, mf := range mfs {
		if mf.GetName() == name {
			return mf
		}
	}
	return nil
}

func names(mfs []*dto.MetricFamily) []string {
	out := make([]string, len(mfs))
	for i, mf := range mfs {
		out[i] = mf.GetName()
	}
	return out
}

func label(m *dto.Metric, name string) string {
	for _, l := range m.GetLabel() {
		if l.GetName() == name {
			return l.GetValue()
		}
	}
	return ""
}
