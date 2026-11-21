package outbox

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/Mars-08042/MarsChat/pkg/kafka"
)

// 带 TraceParent 的事件开 publish Span：父 Span 是写入时的 Span，起点是写入时间，
// 记录头 traceparent 指向 publish Span；没有 TraceParent 的事件不开 Span、不加头。
func TestPublishSpans(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	defer func() { _ = tp.Shutdown(context.Background()) }()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	defer otel.SetTracerProvider(prev)

	ctx, parent := tp.Tracer("test").Start(context.Background(), "SendMessage")
	parent.End()
	created := time.Now().Add(-3 * time.Second).Truncate(time.Millisecond)
	events := []Event{
		{ID: 1, Topic: "t", EventKey: "k", EventType: "x.Created", TraceParent: kafka.TraceParent(ctx), CreatedAt: created},
		{ID: 2, Topic: "t", EventKey: "k", EventType: "x.Created", CreatedAt: created},
	}
	recs := toRecords("svc", events)
	spans := startPublishSpans("svc", events, recs)
	if len(spans) != 1 {
		t.Fatalf("started %d spans, want 1", len(spans))
	}
	for _, sp := range spans {
		sp.End()
	}

	// 导出的还有父 Span SendMessage，只看 publish Span
	var got tracetest.SpanStubs
	for _, sp := range exp.GetSpans() {
		if sp.Name != "SendMessage" {
			got = append(got, sp)
		}
	}
	if len(got) != 1 {
		t.Fatalf("exported %d publish spans, want 1", len(got))
	}
	pub := got[0]
	if pub.Name != "publish t" || pub.SpanKind != trace.SpanKindProducer {
		t.Errorf("span = %s (%v), want publish t (producer)", pub.Name, pub.SpanKind)
	}
	if pub.Parent.SpanID() != parent.SpanContext().SpanID() {
		t.Errorf("publish span parent = %v, want %v", pub.Parent.SpanID(), parent.SpanContext().SpanID())
	}
	if !pub.StartTime.Equal(created) {
		t.Errorf("publish span starts at %v, want event creation time %v", pub.StartTime, created)
	}
	header := trace.SpanContextFromContext(kafka.ContextWithTraceParent(context.Background(), kafka.Header(recs[0], kafka.HeaderTraceParent)))
	if header.SpanID() != pub.SpanContext.SpanID() {
		t.Error("record traceparent does not point to the publish span")
	}
	if kafka.Header(recs[1], kafka.HeaderTraceParent) != "" {
		t.Error("event without TraceParent got a traceparent header")
	}
}
