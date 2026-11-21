package telemetry

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

func TestLogHandlerAddsTraceIDs(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(LogHandler(slog.NewTextHandler(&buf, nil))).With(slog.String("service.name", "test"))

	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{0xab},
		SpanID:     trace.SpanID{0xcd},
		TraceFlags: trace.FlagsSampled,
	})
	logger.InfoContext(trace.ContextWithSpanContext(context.Background(), sc), "with span")
	logger.InfoContext(context.Background(), "without span")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines:\n%s", len(lines), buf.String())
	}
	if !strings.Contains(lines[0], "trace_id="+sc.TraceID().String()) || !strings.Contains(lines[0], "span_id="+sc.SpanID().String()) {
		t.Errorf("line with span lacks trace ids: %s", lines[0])
	}
	// With 加的属性在包装之后仍保留
	if !strings.Contains(lines[0], "service.name=test") {
		t.Errorf("attributes from With lost: %s", lines[0])
	}
	if strings.Contains(lines[1], "trace_id") {
		t.Errorf("line without span has trace_id: %s", lines[1])
	}
}
