package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/go-kratos/kratos/contrib/registry/consul/v3"
	"github.com/hashicorp/consul/api"

	// 修复 Kratos v3.0.0 gRPC 负载均衡器的全局选择器为 nil 的问题，见 pkg/balancer。
	// 放在这里是为了让所有使用 bootstrap 的服务都自动带上。
	_ "github.com/Mars-08042/MarsChat/pkg/balancer"
	"github.com/Mars-08042/MarsChat/pkg/conf"
)

// Consul 选出 leader 之前，健康查询会失败。contrib 的 Watch 第一次查询失败时不撤销对这个服务的登记，
// 之后再 Watch 拿到的是一个没有实例、也没有后台轮询的登记，进程内再也发现不了这个服务（TD-44）。
// 所以创建注册中心时先等 leader：注册中心在所有 gRPC 客户端之前创建，客户端第一次查询时已经有 leader。
const (
	// Docker 重启时单节点 Consul 选主实测 5.7 秒、9.2 秒；等不到就启动失败，由重启策略拉起
	consulLeaderTimeout      = time.Minute
	consulLeaderPollInterval = 500 * time.Millisecond
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
// 返回前等待 Consul 选出 leader，见文件开头的说明。
func NewConsulRegistry(c *conf.Registry, logger *slog.Logger) (*consul.Registry, error) {
	cfg := api.DefaultConfig()
	cfg.Address = c.GetConsul().GetAddress()
	cli, err := api.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("create consul client %s: %w", cfg.Address, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), consulLeaderTimeout)
	defer cancel()
	if err := waitForLeader(ctx, cli.Status(), consulLeaderPollInterval, logger); err != nil {
		return nil, fmt.Errorf("consul %s: %w", cfg.Address, err)
	}
	opts := []consul.Option{consul.WithHealthCheck(false)}
	if sec := int(c.GetConsul().GetTcpCheckInterval().AsDuration() / time.Second); sec > 0 {
		opts = []consul.Option{consul.WithHealthCheck(true), consul.WithHealthCheckInterval(sec)}
	}
	return consul.New(cli, opts...), nil
}

// leaderStatus 是 api.Status 中用到的方法，单元测试可以替换。
type leaderStatus interface {
	LeaderWithQueryOptions(q *api.QueryOptions) (string, error)
}

// waitForLeader 轮询 /v1/status/leader，直到 Consul 有 leader 或 ctx 结束。
// Consul 不可达和还没有 leader 一样处理：容器同时启动时 agent 可能还没开始监听。
// 只在第一次没等到时打一条日志，等到后再打一条，避免启动期间刷屏。
func waitForLeader(ctx context.Context, s leaderStatus, interval time.Duration, logger *slog.Logger) error {
	var last error
	for {
		leader, err := s.LeaderWithQueryOptions((&api.QueryOptions{}).WithContext(ctx))
		if err == nil && leader != "" {
			if last != nil {
				logger.Info("consul leader elected", slog.String("leader", leader))
			}
			return nil
		}
		// 超时打断的是这一次请求，报告上一次的原因（没有 leader 或连不上）更有用
		if ctx.Err() != nil && last != nil {
			return fmt.Errorf("wait for leader: %w", last)
		}
		if err == nil {
			err = errors.New("no cluster leader")
		}
		if last == nil {
			logger.Warn("waiting for consul leader", slog.Any("error", err))
		}
		last = err
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for leader: %w", last)
		case <-time.After(interval):
		}
	}
}
