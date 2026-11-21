package balancer_test

import (
	"context"
	"testing"
	"time"

	"github.com/go-kratos/kratos/v3/registry"
	kgrpc "github.com/go-kratos/kratos/v3/transport/grpc"
	"google.golang.org/grpc"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	// 被测包本来就会链接进测试二进制，这里显式导入只为表明依赖关系。
	// 不导入时会 panic 的反例无法在本包内演示，已在独立的临时包中验证过（2026-11-18）。
	_ "github.com/Mars-08042/MarsChat/pkg/balancer"
)

// staticDiscovery 固定返回一组实例，用来在进程内走通服务发现路径。
type staticDiscovery struct {
	instances []*registry.ServiceInstance
}

func (d staticDiscovery) GetService(context.Context, string) ([]*registry.ServiceInstance, error) {
	return d.instances, nil
}

func (d staticDiscovery) Watch(ctx context.Context, _ string) (registry.Watcher, error) {
	ctx, cancel := context.WithCancel(ctx)
	return &staticWatcher{ctx: ctx, cancel: cancel, instances: d.instances}, nil
}

type staticWatcher struct {
	ctx       context.Context
	cancel    context.CancelFunc
	instances []*registry.ServiceInstance
	sent      bool
}

// Next 第一次返回全部实例，之后阻塞到 Stop 或 ctx 结束。
func (w *staticWatcher) Next() ([]*registry.ServiceInstance, error) {
	if !w.sent {
		w.sent = true
		return w.instances, nil
	}
	<-w.ctx.Done()
	return nil, w.ctx.Err()
}

func (w *staticWatcher) Stop() error {
	w.cancel()
	return nil
}

// 通过 discovery:/// 地址调用会经过 Kratos 的负载均衡器选节点；
// 没有本包时这里会在 balancerBuilder.Build 中空指针 panic。
func TestDiscoveryCallUsesGlobalSelector(t *testing.T) {
	srv := kgrpc.NewServer(kgrpc.Address("127.0.0.1:0"))
	endpoint, err := srv.Endpoint()
	if err != nil {
		t.Fatalf("server endpoint: %v", err)
	}
	go func() { _ = srv.Start(context.Background()) }()
	t.Cleanup(func() { _ = srv.Stop(context.Background()) })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	d := staticDiscovery{instances: []*registry.ServiceInstance{{
		ID:        "test-1",
		Name:      "test",
		Endpoints: []string{endpoint.String()},
	}}}
	conn, err := kgrpc.NewClient(ctx, kgrpc.WithEndpoint("discovery:///test"), kgrpc.WithDiscovery(d))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	resp, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{}, grpc.WaitForReady(true))
	if err != nil {
		t.Fatalf("health check: %v", err)
	}
	if resp.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("status = %s, want SERVING", resp.GetStatus())
	}
}
