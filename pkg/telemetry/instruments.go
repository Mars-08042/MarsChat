package telemetry

import (
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
)

// 以下函数供各包在包级变量中创建仪表。名字按 Prometheus 习惯直接用下划线：
// 计数器导出时自动加 _total，耗时直方图的名字自带 _seconds（单位 s，不会重复加后缀）。
// 标签只能用取值有限的字段（操作名、结果、原因、关闭码），不能放用户 ID、连接 ID、地址。
//
// 创建失败只可能是名字非法等编程错误：交给 otel.Handle 记日志，返回空实现，调用方不用判断。

// Int64Counter 创建只增计数器。
func Int64Counter(name, desc string) metric.Int64Counter {
	c, err := otel.Meter(ScopeName).Int64Counter(name, metric.WithDescription(desc))
	if err != nil {
		otel.Handle(err)
		return noop.Int64Counter{}
	}
	return c
}

// Int64UpDownCounter 创建可增可减的计数器，例如在线连接数。
func Int64UpDownCounter(name, desc string) metric.Int64UpDownCounter {
	c, err := otel.Meter(ScopeName).Int64UpDownCounter(name, metric.WithDescription(desc))
	if err != nil {
		otel.Handle(err)
		return noop.Int64UpDownCounter{}
	}
	return c
}

// SecondsHistogram 创建耗时直方图（秒），桶边界为 LatencyBuckets。
func SecondsHistogram(name, desc string) metric.Float64Histogram {
	h, err := otel.Meter(ScopeName).Float64Histogram(name,
		metric.WithDescription(desc),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(LatencyBuckets...),
	)
	if err != nil {
		otel.Handle(err)
		return noop.Float64Histogram{}
	}
	return h
}
