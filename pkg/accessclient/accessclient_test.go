package accessclient

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	accessv1 "github.com/Mars-08042/MarsChat/api/access/v1"
)

// fakeAccess 是 access 内部接口的假实现，err 非空时 Push 返回它。
type fakeAccess struct {
	accessv1.UnimplementedAccessInternalServiceServer
	err error
}

func (f *fakeAccess) Push(_ context.Context, req *accessv1.PushRequest) (*accessv1.PushResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &accessv1.PushResponse{Delivered: int32(len(req.GetConnectionIds()))}, nil
}

func startFakeAccess(t *testing.T, f *fakeAccess) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := grpc.NewServer()
	accessv1.RegisterAccessInternalServiceServer(s, f)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)
	return lis.Addr().String()
}

// closedAddr 返回一个当前没有进程监听的本地地址。
func closedAddr(t *testing.T) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := lis.Addr().String()
	_ = lis.Close()
	return addr
}

func newTestClient(t *testing.T, cooldown time.Duration) *Client {
	t.Helper()
	p := New(500*time.Millisecond, func() time.Duration { return cooldown }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(p.Close)
	return p
}

var frame = &accessv1.ServerFrame{Body: &accessv1.ServerFrame_Pong{Pong: &accessv1.Pong{}}}

func TestClientEvictsUnreachableInstanceAndCoolsDown(t *testing.T) {
	p := newTestClient(t, 5*time.Second)
	base := time.Now()
	p.now = func() time.Time { return base }
	addr := closedAddr(t)

	if _, err := p.Push(context.Background(), addr, []int64{1}, frame); err == nil {
		t.Fatal("push to a closed port succeeded")
	}
	if _, ok := p.conns[addr]; ok {
		t.Error("connection to the unreachable instance still cached")
	}

	start := time.Now()
	_, err := p.Push(context.Background(), addr, []int64{1}, frame)
	if !errors.Is(err, ErrCoolingDown) {
		t.Fatalf("err during cooldown = %v, want ErrCoolingDown", err)
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Errorf("skipped push took %v, want immediate", d)
	}

	// 冷却结束后重新拨号（仍然不可达，于是再次进入冷却）
	p.now = func() time.Time { return base.Add(6 * time.Second) }
	if _, err := p.Push(context.Background(), addr, []int64{1}, frame); errors.Is(err, ErrCoolingDown) || err == nil {
		t.Fatalf("err after cooldown = %v, want a dial error", err)
	}
	if until := p.cooling[addr]; !until.Equal(base.Add(11 * time.Second)) {
		t.Errorf("cooldown until %v, want %v", until, base.Add(11*time.Second))
	}
}

func TestClientKeepsConnectionOnBusinessError(t *testing.T) {
	p := newTestClient(t, 5*time.Second)
	addr := startFakeAccess(t, &fakeAccess{err: status.Error(codes.InvalidArgument, "bad request")})

	if _, err := p.Push(context.Background(), addr, []int64{1}, frame); err == nil {
		t.Fatal("want the business error")
	}
	if _, ok := p.conns[addr]; !ok {
		t.Error("connection evicted on a business error")
	}
	if _, ok := p.cooling[addr]; ok {
		t.Error("instance cooling down after a business error")
	}
}

func TestClientReusesHealthyConnection(t *testing.T) {
	p := newTestClient(t, 5*time.Second)
	addr := startFakeAccess(t, &fakeAccess{})
	for i := 0; i < 2; i++ {
		n, err := p.Push(context.Background(), addr, []int64{1, 2}, frame)
		if err != nil || n != 2 {
			t.Fatalf("push %d = %d, %v", i, n, err)
		}
	}
	if len(p.conns) != 1 {
		t.Errorf("cached connections = %d, want 1", len(p.conns))
	}
}

// 冷却期为 0 时只摘除连接，下一次调用立即重新拨号。
func TestClientZeroCooldownOnlyEvicts(t *testing.T) {
	p := newTestClient(t, 0)
	addr := closedAddr(t)
	if _, err := p.Push(context.Background(), addr, []int64{1}, frame); err == nil {
		t.Fatal("push to a closed port succeeded")
	}
	if _, ok := p.cooling[addr]; ok {
		t.Error("cooldown recorded although cooldown is 0")
	}
	if _, err := p.Push(context.Background(), addr, []int64{1}, frame); errors.Is(err, ErrCoolingDown) {
		t.Error("push skipped although cooldown is 0")
	}
}
