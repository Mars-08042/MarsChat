// Package telemetrytest 供单元测试读取指标：把带 ManualReader 的 MeterProvider 设为全局，
// 各包在包级变量中经全局 Meter 创建的仪表都会转发到它。
//
// 全局 Meter 只在第一次设置 MeterProvider 时绑定，所以每个测试二进制只设置一次（sync.Once）。
// 计数器是累计值，测试应比较操作前后的差值，不要假设从 0 开始。
package telemetrytest

import (
	"context"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

var (
	once   sync.Once
	reader *sdkmetric.ManualReader
)

// Reader 返回全局共享的 ManualReader，第一次调用时设置全局 MeterProvider。
func Reader(t testing.TB) *sdkmetric.ManualReader {
	t.Helper()
	once.Do(func() {
		reader = sdkmetric.NewManualReader()
		otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	})
	return reader
}

// Collect 读取当前全部指标。
func Collect(t testing.TB) metricdata.ResourceMetrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := Reader(t).Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}
	return rm
}

// Sum 返回名为 name 的计数器（含 UpDownCounter）中标签包含 attrs 的数据点之和，没有数据时为 0。
func Sum(t testing.TB, name string, attrs ...attribute.KeyValue) int64 {
	t.Helper()
	var total int64
	for _, sm := range Collect(t).ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			s, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("metric %s is %T, not an int64 sum", name, m.Data)
			}
			for _, dp := range s.DataPoints {
				if hasAll(dp.Attributes, attrs) {
					total += dp.Value
				}
			}
		}
	}
	return total
}

// HistogramCount 返回名为 name 的直方图中标签包含 attrs 的样本数之和。
func HistogramCount(t testing.TB, name string, attrs ...attribute.KeyValue) uint64 {
	t.Helper()
	var total uint64
	for _, sm := range Collect(t).ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			h, ok := m.Data.(metricdata.Histogram[float64])
			if !ok {
				t.Fatalf("metric %s is %T, not a float64 histogram", name, m.Data)
			}
			for _, dp := range h.DataPoints {
				if hasAll(dp.Attributes, attrs) {
					total += dp.Count
				}
			}
		}
	}
	return total
}

// Gauge 返回名为 name 的 int64 仪表盘中标签包含 attrs 的数据点之和。
func Gauge(t testing.TB, name string, attrs ...attribute.KeyValue) int64 {
	t.Helper()
	var total int64
	for _, sm := range Collect(t).ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			g, ok := m.Data.(metricdata.Gauge[int64])
			if !ok {
				t.Fatalf("metric %s is %T, not an int64 gauge", name, m.Data)
			}
			for _, dp := range g.DataPoints {
				if hasAll(dp.Attributes, attrs) {
					total += dp.Value
				}
			}
		}
	}
	return total
}

func hasAll(set attribute.Set, attrs []attribute.KeyValue) bool {
	for _, a := range attrs {
		v, ok := set.Value(a.Key)
		if !ok || v != a.Value {
			return false
		}
	}
	return true
}
