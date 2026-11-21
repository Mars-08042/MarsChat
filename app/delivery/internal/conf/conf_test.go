package conf_test

import (
	"testing"
	"time"

	"github.com/Mars-08042/MarsChat/app/delivery/internal/conf"
	"github.com/Mars-08042/MarsChat/pkg/bootstrap"
)

// 确保仓库里的配置文件能按 protojson 规则解析，例如时长不能写成 500ms。
func TestConfigFileParses(t *testing.T) {
	var bc conf.Bootstrap
	c, err := bootstrap.LoadConfig("../../configs", &bc)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	defer c.Close()
	if bc.GetServer().GetGrpc().GetAddr() == "" || bc.GetKafka().GetBrokers() == "" || bc.GetSocial().GetEndpoint() == "" {
		t.Error("config not fully loaded: server.grpc.addr, kafka.brokers or social.endpoint is empty")
	}
	if got := bc.GetPush().GetTimeout().AsDuration(); got != 2*time.Second {
		t.Errorf("push.timeout = %v, want 2s", got)
	}
	if got := bc.GetPolicy().GetPushCooldown().AsDuration(); got != 5*time.Second {
		t.Errorf("policy.push_cooldown = %v, want 5s", got)
	}
	if bc.GetServer().GetHttp().GetAddr() == "" {
		t.Error("server.http.addr is empty: /metrics and the Consul address need the HTTP server")
	}
	// 默认不导出 Trace；采样比例来自占位符替换后的字符串，确认能解析成 double
	if bc.GetTelemetry().GetOtlpEndpoint() != "" {
		t.Errorf("telemetry.otlp_endpoint = %q, want empty by default", bc.GetTelemetry().GetOtlpEndpoint())
	}
	if bc.GetTelemetry().SampleRatio == nil || bc.GetTelemetry().GetSampleRatio() != 1 {
		t.Errorf("telemetry.sample_ratio = %v, want 1", bc.GetTelemetry().SampleRatio)
	}
}

// 环境变量覆盖采样比例与 OTLP 地址，越界的采样比例被校验拒绝。
func TestTelemetryEnvOverride(t *testing.T) {
	t.Setenv("MARSCHAT_OTLP_ENDPOINT", "jaeger:4317")
	t.Setenv("MARSCHAT_TRACE_SAMPLE_RATIO", "0.05")
	var bc conf.Bootstrap
	c, err := bootstrap.LoadConfig("../../configs", &bc)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	c.Close()
	if got := bc.GetTelemetry().GetOtlpEndpoint(); got != "jaeger:4317" {
		t.Errorf("telemetry.otlp_endpoint = %q, want jaeger:4317", got)
	}
	if got := bc.GetTelemetry().GetSampleRatio(); got != 0.05 {
		t.Errorf("telemetry.sample_ratio = %v, want 0.05", got)
	}

	t.Setenv("MARSCHAT_TRACE_SAMPLE_RATIO", "1.5")
	var bad conf.Bootstrap
	if c, err := bootstrap.LoadConfig("../../configs", &bad); err == nil {
		c.Close()
		t.Error("LoadConfig accepted telemetry.sample_ratio = 1.5")
	}
}
