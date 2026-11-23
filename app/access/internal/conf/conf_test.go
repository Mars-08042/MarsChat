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
	if got := bc.GetSocial().GetEndpoint(); got != "discovery:///marschat-social" {
		t.Errorf("social.endpoint = %q, want discovery:///marschat-social", got)
	}
	if got := bc.GetCall().GetRingTimeout().AsDuration(); got != 60*time.Second {
		t.Errorf("call.ring_timeout = %v, want 60s", got)
	}
	// 没有设置 MARSCHAT_TURN_URLS 时不下发 TURN，开发环境不依赖 coturn
	if got := bc.GetCall().GetTurn().GetUrls(); got != "" {
		t.Errorf("call.turn.urls = %q, want empty by default", got)
	}
	if got := bc.GetCall().GetTurn().GetTtl().AsDuration(); got != 12*time.Hour {
		t.Errorf("call.turn.ttl = %v, want 12h", got)
	}
}

// 环境变量填了 TURN 地址时，配置能读到；配置了地址却没有密钥，校验必须拒绝。
func TestTurnConfig(t *testing.T) {
	t.Setenv(bootstrap.EnvPrefix+"JWT_PUBLIC_KEY", "test-public-key")
	t.Setenv(bootstrap.EnvPrefix+"TURN_URLS", "turn:192.0.2.10:13478?transport=udp,turn:192.0.2.10:13478?transport=tcp")
	t.Setenv(bootstrap.EnvPrefix+"TURN_SECRET", "s3cret")
	var bc conf.Bootstrap
	c, err := bootstrap.LoadConfig("../../configs", &bc)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	defer c.Close()
	if got := bc.GetCall().GetTurn().GetSecret(); got != "s3cret" {
		t.Errorf("call.turn.secret = %q, want s3cret", got)
	}

	for _, tc := range []struct {
		name string
		turn *conf.Turn
		ok   bool
	}{
		{"disabled", &conf.Turn{}, true},
		{"udp and tcp", &conf.Turn{Urls: "turn:a:3478?transport=udp, turns:a:5349?transport=tcp", Secret: "s"}, true},
		{"stun", &conf.Turn{Urls: "stun:a:3478", Secret: "s"}, true},
		{"missing secret", &conf.Turn{Urls: "turn:a:3478"}, false},
		{"wrong scheme", &conf.Turn{Urls: "http://a:3478", Secret: "s"}, false},
		{"empty item", &conf.Turn{Urls: "turn:a:3478,,turn:b:3478", Secret: "s"}, false},
		{"ttl too short", &conf.Turn{Urls: "turn:a:3478", Secret: "s", Ttl: durationpb.New(time.Second)}, false},
	} {
		err := protovalidate.Validate(tc.turn)
		if (err == nil) != tc.ok {
			t.Errorf("%s: err = %v, want ok=%v", tc.name, err, tc.ok)
		}
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
