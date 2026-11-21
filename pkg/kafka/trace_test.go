package kafka

import (
	"context"
	"errors"
	"testing"

	"github.com/twmb/franz-go/pkg/kgo"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/Mars-08042/MarsChat/pkg/telemetry/telemetrytest"
)

// 全局 TracerProvider 在测试二进制中只设置一次，各用例按名字筛选自己的 Span。
var spans = func() *tracetest.InMemoryExporter {
	exp := tracetest.NewInMemoryExporter()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp)))
	return exp
}()

func TestTraceParentRoundTrip(t *testing.T) {
	ctx, parent := otel.Tracer("test").Start(context.Background(), "parent")
	defer parent.End()
	tp := TraceParent(ctx)
	if len(tp) != 55 {
		t.Fatalf("TraceParent = %q, want a 55-character W3C traceparent", tp)
	}
	got := trace.SpanContextFromContext(ContextWithTraceParent(context.Background(), tp))
	if got.TraceID() != parent.SpanContext().TraceID() || got.SpanID() != parent.SpanContext().SpanID() || !got.IsRemote() {
		t.Errorf("parsed span context %v, want remote copy of %v", got, parent.SpanContext())
	}
	if TraceParent(context.Background()) != "" {
		t.Error("TraceParent without a span is not empty")
	}
	if ContextWithTraceParent(context.Background(), "garbage") != context.Background() {
		t.Error("invalid traceparent changed the context")
	}
}

// 消费 Span 接在记录头的上游 Span 之下，处理器拿到的 ctx 带着消费 Span；转入死信队列时 traceparent 原样保留。
func TestConsumeSpanContinuesTrace(t *testing.T) {
	telemetrytest.Reader(t)
	ctx, parent := otel.Tracer("test").Start(context.Background(), "publish")
	parent.End()

	c, s := newTestConsumer()
	c.group = "trace_test"
	var handlerSpan trace.SpanContext
	calls := 0
	c.Handle("x", func(ctx context.Context, _ *Event) error {
		handlerSpan = trace.SpanContextFromContext(ctx)
		calls++
		if calls == 1 {
			return errors.New("temporary")
		}
		return Permanent(errors.New("bad payload"))
	})
	r := record("x", nil)
	r.Topic = "trace.topic"
	InjectTrace(ctx, r)
	if !c.process(context.Background(), r) {
		t.Fatal("process returned false")
	}

	var consume *tracetest.SpanStub
	for _, sp := range spans.GetSpans() {
		if sp.Name == "consume trace.topic" {
			consume = &sp
		}
	}
	if consume == nil {
		t.Fatal("consume span not exported")
	}
	if consume.Parent.SpanID() != parent.SpanContext().SpanID() || consume.SpanContext.TraceID() != parent.SpanContext().TraceID() {
		t.Errorf("consume span parent = %v, want %v", consume.Parent, parent.SpanContext())
	}
	if consume.SpanKind != trace.SpanKindConsumer {
		t.Errorf("span kind = %v, want consumer", consume.SpanKind)
	}
	if handlerSpan.SpanID() != consume.SpanContext.SpanID() {
		t.Error("handler context does not carry the consume span")
	}
	if len(consume.Events) != 2 {
		t.Errorf("recorded %d error events, want 2 (one per failed attempt)", len(consume.Events))
	}

	recs := s.records()
	if len(recs) != 1 || recs[0].Topic != TopicDLQ {
		t.Fatalf("written records = %v, want one dead letter", recs)
	}
	if Header(recs[0], HeaderTraceParent) != Header(r, HeaderTraceParent) {
		t.Error("dead letter record lost the traceparent header")
	}

	group := attribute.String("group", "trace_test")
	topic := attribute.String("topic", "trace.topic")
	for result, want := range map[string]int64{"error": 1, "dlq": 1, "ok": 0} {
		if got := telemetrytest.Sum(t, "marschat_kafka_consumed", group, topic, attribute.String("result", result)); got != want {
			t.Errorf("consumed{result=%s} = %d, want %d", result, got, want)
		}
	}
	if got := telemetrytest.HistogramCount(t, "marschat_kafka_handle_seconds", group, topic); got != 2 {
		t.Errorf("handle samples = %d, want 2", got)
	}
}

// 没有 traceparent 头的记录开一条新 Trace，不报错。
func TestConsumeWithoutTraceParent(t *testing.T) {
	c, _ := newTestConsumer()
	var got trace.SpanContext
	c.Handle("x", func(ctx context.Context, _ *Event) error {
		got = trace.SpanContextFromContext(ctx)
		return nil
	})
	r := &kgo.Record{Topic: "no.parent", Headers: []kgo.RecordHeader{{Key: HeaderEventType, Value: []byte("x")}}}
	if !c.process(context.Background(), r) {
		t.Fatal("process returned false")
	}
	if !got.IsValid() || got.IsRemote() {
		t.Errorf("handler span context = %v, want a new local root span", got)
	}
}
