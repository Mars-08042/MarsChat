package kafka

import (
	"context"

	"github.com/twmb/franz-go/pkg/kgo"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/Mars-08042/MarsChat/pkg/telemetry"
)

// 跨 Kafka 传播固定用 W3C Trace Context，不依赖全局传播器：Outbox 表里存的就是 traceparent 字符串。
var traceContext = propagation.TraceContext{}

// 指标，标签 group 为消费组（重试消费者带 -retry 后缀），topic 为记录所在 topic：
//   - marschat_kafka_consumed_total{result}：ok 处理成功；error 一次处理失败（原地重试会重复计数）；
//     retry 转入重试队列；dlq 写入死信队列；
//   - marschat_kafka_handle_seconds：单次处理耗时，包括失败的尝试。
var (
	consumedCounter = telemetry.Int64Counter("marschat_kafka_consumed", "Kafka 记录处理次数，按结果区分")
	handleDuration  = telemetry.SecondsHistogram("marschat_kafka_handle_seconds", "Kafka 记录单次处理耗时")
)

// recordCarrier 让 OTel 传播器读写记录头。
type recordCarrier struct{ r *kgo.Record }

func (c recordCarrier) Get(key string) string { return Header(c.r, key) }

func (c recordCarrier) Set(key, value string) {
	for i, h := range c.r.Headers {
		if h.Key == key {
			c.r.Headers[i].Value = []byte(value)
			return
		}
	}
	c.r.Headers = append(c.r.Headers, kgo.RecordHeader{Key: key, Value: []byte(value)})
}

func (c recordCarrier) Keys() []string {
	keys := make([]string, len(c.r.Headers))
	for i, h := range c.r.Headers {
		keys[i] = h.Key
	}
	return keys
}

// InjectTrace 把 ctx 中的 Span 上下文写入记录头 traceparent；ctx 中没有 Span 时不写。
func InjectTrace(ctx context.Context, r *kgo.Record) {
	traceContext.Inject(ctx, recordCarrier{r: r})
}

// ExtractTrace 从记录头恢复上游的 Span 上下文，没有或无法解析时返回原 ctx。
func ExtractTrace(ctx context.Context, r *kgo.Record) context.Context {
	return traceContext.Extract(ctx, recordCarrier{r: r})
}

// TraceParent 返回 ctx 中 Span 上下文的 traceparent 字符串，没有 Span 时为空。
func TraceParent(ctx context.Context) string {
	carrier := propagation.MapCarrier{}
	traceContext.Inject(ctx, carrier)
	return carrier[HeaderTraceParent]
}

// ContextWithTraceParent 把 traceparent 字符串解析为远程父 Span 上下文，为空或无法解析时返回原 ctx。
func ContextWithTraceParent(ctx context.Context, traceParent string) context.Context {
	if traceParent == "" {
		return ctx
	}
	return traceContext.Extract(ctx, propagation.MapCarrier{HeaderTraceParent: traceParent})
}

func tracer() trace.Tracer {
	return otel.Tracer(telemetry.ScopeName)
}
