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
}
