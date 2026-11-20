package server

import (
	"context"
	"testing"
	"time"

	kgrpc "github.com/go-kratos/kratos/v3/transport/grpc"
	ggrpc "google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
)

type fakeDrainer struct{ ch chan struct{} }

func (f fakeDrainer) Drained() <-chan struct{} { return f.ch }

// startGRPC 启动一个 Kratos gRPC 服务器，返回包装后的服务器和连到它的健康检查客户端。
func startGRPC(t *testing.T, drained chan struct{}) (*DrainingGRPCServer, grpc_health_v1.HealthClient) {
	t.Helper()
	gs := kgrpc.NewServer(kgrpc.Address("127.0.0.1:0"))
	s := &DrainingGRPCServer{Server: gs, hub: fakeDrainer{ch: drained}}
	u, err := s.Endpoint()
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.Start(context.Background()) }()
	conn, err := ggrpc.NewClient(u.Host, ggrpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return s, grpc_health_v1.NewHealthClient(conn)
}

func check(ctx context.Context, c grpc_health_v1.HealthClient) error {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	_, err := c.Check(ctx, &grpc_health_v1.HealthCheckRequest{}, ggrpc.WaitForReady(true))
	return err
}

// Hub 排空之前 gRPC 服务器继续处理请求（推送仍然有效），排空后才停止。
func TestDrainingGRPCServerWaitsForHub(t *testing.T) {
	drained := make(chan struct{})
	s, health := startGRPC(t, drained)
	if err := check(context.Background(), health); err != nil {
		t.Fatalf("health check before stop: %v", err)
	}

	stopped := make(chan struct{})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.Stop(ctx)
		close(stopped)
	}()

	select {
	case <-stopped:
		t.Fatal("gRPC server stopped before the hub drained")
	case <-time.After(200 * time.Millisecond):
	}
	if err := check(context.Background(), health); err != nil {
		t.Fatalf("health check while the hub is draining: %v", err)
	}

	close(drained)
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("gRPC server did not stop after the hub drained")
	}
}

// 停机超时到期时不再等 Hub，直接停止。
func TestDrainingGRPCServerStopsOnDeadline(t *testing.T) {
	s, _ := startGRPC(t, make(chan struct{}))
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() {
		_ = s.Stop(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("gRPC server did not stop after the deadline")
	}
}
