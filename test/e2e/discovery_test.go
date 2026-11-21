//go:build e2e

// Package e2e 是依赖 compose 环境和已启动服务的端到端测试。
// 运行方式：make up，再启动各服务（make run-account、make run-social），最后 make e2e。
package e2e

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/go-kratos/kratos/contrib/registry/consul/v3"
	"github.com/go-kratos/kratos/v3/registry"
	kgrpc "github.com/go-kratos/kratos/v3/transport/grpc"
	"github.com/hashicorp/consul/api"
	"google.golang.org/grpc"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	// 通过 discovery:/// 调用前必须设置全局选择器，见 pkg/balancer
	_ "github.com/Mars-08042/MarsChat/pkg/balancer"
)

var services = []string{"marschat-account", "marschat-social"}

func newRegistry(t *testing.T) *consul.Registry {
	t.Helper()
	cfg := api.DefaultConfig()
	cfg.Address = "127.0.0.1:18500"
	if v := os.Getenv("MARSCHAT_CONSUL_ADDR"); v != "" {
		cfg.Address = v
	}
	cli, err := api.NewClient(cfg)
	if err != nil {
		t.Fatalf("create consul client: %v", err)
	}
	return consul.New(cli)
}

// 服务注册到 Consul 后，可以通过 discovery:/// 地址发现并调用 gRPC 健康检查，
// 注册信息里的 HTTP 端点就绪探针返回 200。
func TestServiceDiscovery(t *testing.T) {
	r := newRegistry(t)
	for _, name := range services {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			conn, err := kgrpc.NewClient(ctx, kgrpc.WithEndpoint("discovery:///"+name), kgrpc.WithDiscovery(r))
			if err != nil {
				t.Fatalf("dial %s: %v", name, err)
			}
			defer conn.Close()
			// 解析器拿到实例地址前连接处于 CONNECTING，WaitForReady 让调用等待而不是立即失败
			resp, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{}, grpc.WaitForReady(true))
			if err != nil {
				t.Fatalf("grpc health check: %v", err)
			}
			if resp.GetStatus() != healthpb.HealthCheckResponse_SERVING {
				t.Fatalf("grpc health status = %s, want SERVING", resp.GetStatus())
			}

			instances, err := r.GetService(ctx, name)
			if err != nil {
				t.Fatalf("get service: %v", err)
			}
			endpoint := httpEndpoint(t, instances)
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/readyz", nil)
			if err != nil {
				t.Fatal(err)
			}
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("GET %s/readyz: %v", endpoint, err)
			}
			defer res.Body.Close()
			if res.StatusCode != http.StatusOK {
				t.Fatalf("GET %s/readyz status = %d, want 200", endpoint, res.StatusCode)
			}
		})
	}
}

func httpEndpoint(t *testing.T, instances []*registry.ServiceInstance) string {
	t.Helper()
	for _, ins := range instances {
		for _, e := range ins.Endpoints {
			u, err := url.Parse(e)
			if err == nil && u.Scheme == "http" {
				return "http://" + u.Host
			}
		}
	}
	t.Fatalf("no http endpoint registered in %d instance(s)", len(instances))
	return ""
}
