package telemetry

import (
	"fmt"

	"github.com/go-kratos/kratos/contrib/otel/v3/metrics"
	"github.com/go-kratos/kratos/contrib/otel/v3/tracing"
	"github.com/go-kratos/kratos/v3/middleware"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// ServerMiddlewares 返回服务端的链路追踪与请求指标中间件，应放在中间件链最外层：
// Span 覆盖整个请求，访问日志能取到 trace_id；指标在访问日志的 panic 恢复之外，panic 也按 500 计数。
// 指标为 server_requests_seconds{kind,operation} 与 server_requests_code_total{kind,operation,code,reason}。
func ServerMiddlewares() ([]middleware.Middleware, error) {
	requests, seconds, err := requestInstruments(metrics.DefaultServerRequestsCounterName, metrics.DefaultServerSecondsHistogramName)
	if err != nil {
		return nil, err
	}
	return []middleware.Middleware{
		tracing.Server(),
		metrics.Server(metrics.WithRequests(requests), metrics.WithSeconds(seconds)),
	}, nil
}

// ClientMiddlewares 返回 gRPC 客户端的链路追踪与请求指标中间件，应放在重试和熔断之前：
// 耗时与结果按调用方看到的整次调用统计，包括重试。
// 指标为 client_requests_seconds 与 client_requests_code_total，标签同服务端。
func ClientMiddlewares() ([]middleware.Middleware, error) {
	requests, seconds, err := requestInstruments(metrics.DefaultClientRequestsCounterName, metrics.DefaultClientSecondsHistogramName)
	if err != nil {
		return nil, err
	}
	return []middleware.Middleware{
		tracing.Client(),
		metrics.Client(metrics.WithRequests(requests), metrics.WithSeconds(seconds)),
	}, nil
}

// requestInstruments 创建请求计数器与耗时直方图。不用 Kratos 默认的直方图：它的桶上限是 1 秒，
// 上传等慢请求全部落在 +Inf 桶里。同名仪表重复创建时 SDK 返回同一个。
func requestInstruments(counterName, histogramName string) (metric.Int64Counter, metric.Float64Histogram, error) {
	meter := otel.Meter(ScopeName)
	requests, err := meter.Int64Counter(counterName, metric.WithUnit("{call}"))
	if err != nil {
		return nil, nil, fmt.Errorf("create counter %s: %w", counterName, err)
	}
	seconds, err := meter.Float64Histogram(histogramName,
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(LatencyBuckets...),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("create histogram %s: %w", histogramName, err)
	}
	return requests, seconds, nil
}
