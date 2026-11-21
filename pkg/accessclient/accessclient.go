// Package accessclient 按地址直连 access 实例，调用它的内部接口（推送、踢连接）。
// 在线路由（pkg/route）中记录了每条连接所在实例的 gRPC 地址，delivery 推送事件、access 转发通话信令都按它直连。
package accessclient

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/go-kratos/kratos/v3/middleware"
	kgrpc "github.com/go-kratos/kratos/v3/transport/grpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	accessv1 "github.com/Mars-08042/MarsChat/api/access/v1"
	accountv1 "github.com/Mars-08042/MarsChat/api/account/v1"
)

// ErrCoolingDown 表示目标 access 实例刚调用失败、仍在冷却期，这次调用被跳过。
var ErrCoolingDown = errors.New("access instance cooling down after a failed call")

// Client 按地址直连 access 实例，不经 Consul 负载均衡：推送必须到达持有连接的那个实例（ADR-0005）。
// 每个地址一条 gRPC 连接，建立后复用。不加重试和熔断：换实例没有意义，单个实例的失败由下面的冷却处理。
//
// 调用返回 Unavailable 或 DeadlineExceeded 时，说明实例已经退出或不可达：关闭并移出缓存的连接，
// 地址进入冷却期，期间的调用直接返回 ErrCoolingDown。
// 这样 access 实例宕机后，不会每次调用都等一次超时，缓存也不会只增不减（TD-31）；冷却结束后重新拨号。
// 不监听 Consul 中 access 的变化：正在排空的实例已从 Consul 注销，但还在给剩余连接推送。
type Client struct {
	timeout  time.Duration
	cooldown func() time.Duration
	logger   *slog.Logger
	mws      []middleware.Middleware
	now      func() time.Time

	mu      sync.Mutex
	conns   map[string]*grpc.ClientConn
	cooling map[string]time.Time
}

// New 创建客户端缓存。timeout 是单次调用超时；cooldown 每次进入冷却时调用，便于热更新，返回 0 表示只摘除连接、不冷却。
// mws 是每条连接的客户端中间件，例如链路追踪与请求指标。
func New(timeout time.Duration, cooldown func() time.Duration, logger *slog.Logger, mws ...middleware.Middleware) *Client {
	return &Client{
		timeout:  timeout,
		cooldown: cooldown,
		logger:   logger,
		mws:      mws,
		now:      time.Now,
		conns:    make(map[string]*grpc.ClientConn),
		cooling:  make(map[string]time.Time),
	}
}

// Close 关闭全部缓存的连接，供 wire 的 cleanup 使用。
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for addr, conn := range c.conns {
		if err := conn.Close(); err != nil {
			c.logger.Warn("close access client failed", slog.String("addr", addr), slog.Any("error", err))
		}
	}
	clear(c.conns)
}

func (c *Client) client(addr string) (accessv1.AccessInternalServiceClient, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if until, ok := c.cooling[addr]; ok {
		if c.now().Before(until) {
			return nil, ErrCoolingDown
		}
		delete(c.cooling, addr)
	}
	conn, ok := c.conns[addr]
	if !ok {
		// NewClient 不建立连接，持锁调用不会阻塞
		var err error
		conn, err = kgrpc.NewClient(context.Background(),
			kgrpc.WithEndpoint(addr),
			kgrpc.WithTimeout(c.timeout),
			kgrpc.WithMiddleware(c.mws...),
		)
		if err != nil {
			return nil, fmt.Errorf("create access client %s: %w", addr, err)
		}
		c.conns[addr] = conn
	}
	return accessv1.NewAccessInternalServiceClient(conn), nil
}

// unreachable 判断错误是否说明实例不可达。业务错误（如参数校验失败）说明实例是好的，不摘除；
// 调用方自己取消（服务停止）也不算。
func unreachable(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded:
		return true
	}
	return false
}

// evict 在实例不可达时关闭并移出缓存的连接，地址进入冷却期。并发失败的调用只有第一个会关闭连接。
func (c *Client) evict(addr string, err error) {
	if !unreachable(err) {
		return
	}
	now := c.now()
	cooldown := c.cooldown()
	c.mu.Lock()
	conn := c.conns[addr]
	delete(c.conns, addr)
	if cooldown > 0 {
		c.cooling[addr] = now.Add(cooldown)
	}
	// 顺手清掉已过期的冷却记录，宕机实例的地址不会再被访问，不清理会一直留着
	for a, until := range c.cooling {
		if !now.Before(until) {
			delete(c.cooling, a)
		}
	}
	c.mu.Unlock()
	if conn == nil {
		return
	}
	if cerr := conn.Close(); cerr != nil {
		c.logger.Debug("close access client failed", slog.String("addr", addr), slog.Any("error", cerr))
	}
	c.logger.Warn("access instance unreachable, connection evicted",
		slog.String("addr", addr), slog.Duration("cooldown", cooldown), slog.Any("error", err))
}

// Push 把帧推给 addr 实例上的连接，返回实际放入发送队列的连接数。
func (c *Client) Push(ctx context.Context, addr string, connIDs []int64, frame *accessv1.ServerFrame) (int, error) {
	cl, err := c.client(addr)
	if err != nil {
		return 0, err
	}
	resp, err := cl.Push(ctx, &accessv1.PushRequest{ConnectionIds: connIDs, Frame: frame})
	if err != nil {
		c.evict(addr, err)
		return 0, err
	}
	return int(resp.GetDelivered()), nil
}

// Kick 断开 addr 实例上的连接，返回找到的连接数。
func (c *Client) Kick(ctx context.Context, addr string, connIDs []int64, reason accountv1.RevokeReason) (int, error) {
	cl, err := c.client(addr)
	if err != nil {
		return 0, err
	}
	resp, err := cl.Kick(ctx, &accessv1.KickRequest{ConnectionIds: connIDs, Reason: reason})
	if err != nil {
		c.evict(addr, err)
		return 0, err
	}
	return int(resp.GetKicked()), nil
}
