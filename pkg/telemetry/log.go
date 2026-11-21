package telemetry

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

// LogHandler 包装 slog.Handler：日志调用带的 ctx 中有 Span 时追加 trace_id、span_id，
// 用 *Context 系列方法（InfoContext、WarnContext、LogAttrs）记录的日志才能关联到 Trace。
// 没有 Span 或采样前就被丢弃的上下文（无效的 SpanContext）不追加。
func LogHandler(h slog.Handler) slog.Handler {
	return traceHandler{h}
}

type traceHandler struct{ slog.Handler }

func (h traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}
	return h.Handler.Handle(ctx, r)
}

func (h traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return traceHandler{h.Handler.WithAttrs(attrs)}
}

func (h traceHandler) WithGroup(name string) slog.Handler {
	return traceHandler{h.Handler.WithGroup(name)}
}
