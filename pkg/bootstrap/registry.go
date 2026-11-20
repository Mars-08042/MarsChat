package bootstrap

import (
	"fmt"
	"os"
	"time"

	"github.com/go-kratos/kratos/contrib/registry/consul/v3"
	"github.com/hashicorp/consul/api"

	// 修复 Kratos v3.0.0 gRPC 负载均衡器的全局选择器为 nil 的问题，见 pkg/balancer。
	// 放在这里是为了让所有使用 bootstrap 的服务都自动带上。
	_ "github.com/Mars-08042/MarsChat/pkg/balancer"
	"github.com/Mars-08042/MarsChat/pkg/conf"
)

// InstanceID 生成注册到 Consul 的实例 ID。
// Consul 要求同一个 agent 上的实例 ID 唯一（不区分服务名），本地开发时多个服务共用一台主机，
// 只用主机名会互相覆盖，所以拼上服务名和进程号。
func InstanceID(name string) string {
	host, err := os.Hostname()
	if err != nil {
		host = "unknown"
	}
	return fmt.Sprintf("%s-%s-%d", name, host, os.Getpid())
}

// NewConsulRegistry 创建 Consul 注册中心，同时用于服务注册（Registrar）和服务发现（Discovery）。
// 实例始终带 TTL 心跳检查，进程退出后心跳停止，实例变为 critical，不会再被发现。
func NewConsulRegistry(c *conf.Registry) (*consul.Registry, error) {
	cfg := api.DefaultConfig()
	cfg.Address = c.GetConsul().GetAddress()
	cli, err := api.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("create consul client %s: %w", cfg.Address, err)
	}
	opts := []consul.Option{consul.WithHealthCheck(false)}
	if sec := int(c.GetConsul().GetTcpCheckInterval().AsDuration() / time.Second); sec > 0 {
		opts = []consul.Option{consul.WithHealthCheck(true), consul.WithHealthCheckInterval(sec)}
	}
	return consul.New(cli, opts...), nil
}
