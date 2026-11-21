// Package telemetry 初始化指标与链路追踪（OpenTelemetry SDK）。
//
// 指标经 Prometheus exporter 注册到 Prometheus 默认注册表，由各服务 HTTP 端口上的 /metrics 暴露；
// 默认注册表自带 Go 运行时与进程指标。Trace 经 OTLP gRPC 导出到 Jaeger，未配置地址时不导出。
// 业务代码统一通过 otel.Meter / otel.Tracer 取全局实例：全局实例在 Setup 之前创建的仪表
// 也会在 Setup 之后转发到真正的 SDK，所以包级变量可以在 init 阶段创建。
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/otlptranslator"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	promexporter "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/Mars-08042/MarsChat/pkg/conf"
)

// ScopeName 是本项目仪表与 Tracer 的统一名称。
const ScopeName = "github.com/Mars-08042/MarsChat"

// LatencyBuckets 是耗时直方图（秒）的桶边界：覆盖 1 毫秒到 10 秒，压测时 P99 落在桶内才有意义。
var LatencyBuckets = []float64{0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// shutdownTimeout 是停机时刷新剩余 Span 的最长等待时间
const shutdownTimeout = 5 * time.Second

// errorLogInterval 是 SDK 内部错误日志的最小间隔。Jaeger 没有启动时每批导出都会失败，
// 不限频会刷屏；Span 丢弃不影响请求。
const errorLogInterval = time.Minute

// Info 描述当前进程，写入 Trace 的资源属性。
type Info struct {
	Name       string
	Version    string
	InstanceID string
}

// Setup 初始化全局 MeterProvider、TracerProvider 与传播器，返回停机时调用的关闭函数。
// 指标注册到 Prometheus 默认注册表，一个进程只能调用一次。
func Setup(c *conf.Telemetry, info Info, logger *slog.Logger) (func(), error) {
	return setup(c, info, prometheus.DefaultRegisterer, logger)
}

func setup(c *conf.Telemetry, info Info, reg prometheus.Registerer, logger *slog.Logger) (func(), error) {
	otel.SetErrorHandler(newRateLimitedHandler(logger, errorLogInterval))
	// 服务间同步调用由 Kratos tracing 中间件按自己的传播器处理；全局传播器用于 Kafka 记录头
	otel.SetTextMapPropagator(propagation.TraceContext{})

	res := resource.NewSchemaless(
		attribute.String("service.name", info.Name),
		attribute.String("service.version", info.Version),
		attribute.String("service.instance.id", info.InstanceID),
	)

	exporter, err := promexporter.New(
		promexporter.WithRegisterer(reg),
		// 显式指定：Prometheus 风格的名字（下划线、单位后缀、计数器 _total），不随 SDK 默认值变化
		promexporter.WithTranslationStrategy(otlptranslator.UnderscoreEscapingWithSuffixes),
		// 不给每条序列加 otel_scope_* 标签，减少基数；实例信息由 Prometheus 的发现标签提供
		promexporter.WithoutScopeInfo(),
	)
	if err != nil {
		return nil, fmt.Errorf("create prometheus exporter: %w", err)
	}
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter), sdkmetric.WithResource(res))
	otel.SetMeterProvider(mp)

	var tp *sdktrace.TracerProvider
	if ep := c.GetOtlpEndpoint(); ep != "" {
		// 连接是惰性的，Jaeger 不可用不影响启动；导出失败时丢弃 Span
		te, err := otlptracegrpc.New(context.Background(),
			otlptracegrpc.WithEndpoint(ep),
			otlptracegrpc.WithInsecure(),
		)
		if err != nil {
			_ = mp.Shutdown(context.Background())
			return nil, fmt.Errorf("create otlp trace exporter %s: %w", ep, err)
		}
		ratio := sampleRatio(c)
		tp = sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(te),
			sdktrace.WithResource(res),
			sdktrace.WithSampler(newSampler(ratio)),
		)
		otel.SetTracerProvider(tp)
		logger.Info("tracing enabled", slog.String("endpoint", ep), slog.Float64("sample_ratio", ratio))
	}

	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		var errs []error
		if tp != nil {
			errs = append(errs, tp.Shutdown(ctx))
		}
		errs = append(errs, mp.Shutdown(ctx))
		if err := errors.Join(errs...); err != nil {
			logger.Warn("telemetry shutdown failed", slog.Any("error", err))
		}
	}, nil
}

// sampleRatio 返回配置的采样比例，不填时为 1。
func sampleRatio(c *conf.Telemetry) float64 {
	if c != nil && c.SampleRatio != nil {
		return c.GetSampleRatio()
	}
	return 1
}

// newSampler 按比例采样新 Trace；带父 Span 的请求跟随父 Span 的采样决定，一条 Trace 不会只采到一半。
func newSampler(ratio float64) sdktrace.Sampler {
	return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))
}

// MetricsHandler 返回 /metrics 处理函数，输出 Prometheus 默认注册表中的全部指标。
func MetricsHandler() http.Handler {
	return promhttp.Handler()
}

// rateLimitedHandler 把 OTel SDK 的内部错误写成 warn 日志，同一时间窗口内只写一条，并带上被省略的条数。
type rateLimitedHandler struct {
	logger   *slog.Logger
	interval time.Duration

	mu         sync.Mutex
	last       time.Time
	suppressed int
}

func newRateLimitedHandler(logger *slog.Logger, interval time.Duration) *rateLimitedHandler {
	return &rateLimitedHandler{logger: logger, interval: interval}
}

func (h *rateLimitedHandler) Handle(err error) {
	h.mu.Lock()
	now := time.Now()
	if !h.last.IsZero() && now.Sub(h.last) < h.interval {
		h.suppressed++
		h.mu.Unlock()
		return
	}
	suppressed := h.suppressed
	h.last, h.suppressed = now, 0
	h.mu.Unlock()
	h.logger.Warn("telemetry error", slog.Any("error", err), slog.Int("suppressed", suppressed))
}
