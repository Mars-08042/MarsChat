package conf_test

import (
	"testing"

	"github.com/Mars-08042/MarsChat/app/message/internal/conf"
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
	if bc.GetServer().GetGrpc().GetAddr() == "" || bc.GetMysql().GetDsn() == "" || bc.GetKafka().GetBrokers() == "" {
		// 不打印整个配置，DSN 里可能带密码
		t.Error("config not fully loaded: server.grpc.addr, mysql.dsn or kafka.brokers is empty")
	}
	if got := bc.GetPolicy().GetSendMessage().GetLimit(); got != 30 {
		t.Errorf("policy.send_message.limit = %d, want 30", got)
	}
	if got := bc.GetPolicy().GetDifferenceTooLong(); got != 1000 {
		t.Errorf("policy.difference_too_long = %d, want 1000", got)
	}
}
