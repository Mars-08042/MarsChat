package conf_test

import (
	"os"
	"testing"

	"github.com/Mars-08042/MarsChat/app/social/internal/conf"
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
	if bc.GetServer().GetGrpc().GetAddr() == "" || bc.GetMysql().GetDsn() == "" || bc.GetRedis().GetAddr() == "" {
		// 不打印整个配置，DSN 里可能带密码
		t.Error("config not fully loaded: server.grpc.addr, mysql.dsn or redis.addr is empty")
	}
	if got := bc.GetPolicy().GetMaxGroupMembers(); got != 500 {
		t.Errorf("policy.max_group_members = %d, want 500", got)
	}
	if got := bc.GetPolicy().GetFriendRequest().GetLimit(); got != 20 {
		t.Errorf("policy.friend_request.limit = %d, want 20", got)
	}
}

// 默认值本身带冒号，占位符只按第一个冒号切分出变量名。
func TestAccountEndpointDefault(t *testing.T) {
	t.Setenv(bootstrap.EnvPrefix+"JWT_PUBLIC_KEY", "test-public-key")
	// 本机设置了该变量时也要测到默认值；t.Setenv 负责在结束后恢复原值
	t.Setenv(bootstrap.EnvPrefix+"ACCOUNT_ENDPOINT", "")
	os.Unsetenv(bootstrap.EnvPrefix + "ACCOUNT_ENDPOINT")
	var bc conf.Bootstrap
	c, err := bootstrap.LoadConfig("../../configs", &bc)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	defer c.Close()
	if got := bc.GetAccount().GetEndpoint(); got != "discovery:///marschat-account" {
		t.Errorf("account.endpoint = %q, want discovery:///marschat-account", got)
	}
}
