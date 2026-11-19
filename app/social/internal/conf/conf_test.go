package conf_test

import (
	"testing"

	"github.com/Mars-08042/MarsChat/app/social/internal/conf"
	"github.com/Mars-08042/MarsChat/pkg/bootstrap"
)

// 确保仓库里的配置文件能按 protojson 规则解析，例如时长不能写成 500ms。
func TestConfigFileParses(t *testing.T) {
	var bc conf.Bootstrap
	if err := bootstrap.LoadConfig("../../configs", &bc); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if bc.GetServer().GetGrpc().GetAddr() == "" || bc.GetMysql().GetDsn() == "" || bc.GetRedis().GetAddr() == "" {
		// 不打印整个配置，DSN 里可能带密码
		t.Error("config not fully loaded: server.grpc.addr, mysql.dsn or redis.addr is empty")
	}
}
