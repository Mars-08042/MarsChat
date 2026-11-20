package conf_test

import (
	"testing"
	"time"

	"buf.build/go/protovalidate"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/Mars-08042/MarsChat/app/access/internal/conf"
	"github.com/Mars-08042/MarsChat/pkg/bootstrap"
)

// 确保仓库里的配置文件能按 protojson 规则解析，例如时长不能写成 500ms。
// 公钥只从环境变量读取，这里填占位值让校验通过，不使用它。
func TestConfigFileParses(t *testing.T) {
	t.Setenv(bootstrap.EnvPrefix+"JWT_PUBLIC_KEY", "test-public-key")
	var bc conf.Bootstrap
	c, err := bootstrap.LoadConfig("../../configs", &bc)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	defer c.Close()
	if bc.GetServer().GetGrpc().GetAddr() == "" {
		t.Error("config not fully loaded: server.grpc.addr is empty")
	}
	// 默认留空，由注册的 gRPC 端点决定，见 data.RouteRepo.ResolveAddr
	if got := bc.GetAdvertiseAddr(); got != "" {
		t.Errorf("advertise_addr = %q, want empty by default", got)
	}
	if got := bc.GetWebsocket().GetHeartbeatTimeout().AsDuration(); got != 90*time.Second {
		t.Errorf("websocket.heartbeat_timeout = %v, want 90s", got)
	}
	if got := bc.GetWebsocket().GetDrainWindow().AsDuration(); got != 5*time.Second {
		t.Errorf("websocket.drain_window = %v, want 5s", got)
	}
	if got := bc.GetPolicy().GetFramesPerSecond(); got != 30 {
		t.Errorf("policy.frames_per_second = %d, want 30", got)
	}
}

// 心跳超时不大于心跳间隔时，客户端按间隔发心跳也会被断开，配置校验必须拒绝。
func TestHeartbeatTimeoutMustExceedInterval(t *testing.T) {
	ws := &conf.WebSocket{
		AuthTimeout:       durationpb.New(10 * time.Second),
		HeartbeatInterval: durationpb.New(30 * time.Second),
		HeartbeatTimeout:  durationpb.New(30 * time.Second),
		SendQueue:         256,
		WriteTimeout:      durationpb.New(10 * time.Second),
		MaxFrameBytes:     65536,
		DrainWindow:       durationpb.New(5 * time.Second),
	}
	if err := protovalidate.Validate(ws); err == nil {
		t.Fatal("want validation error when heartbeat_timeout == heartbeat_interval")
	}
	ws.HeartbeatTimeout = durationpb.New(90 * time.Second)
	if err := protovalidate.Validate(ws); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}
