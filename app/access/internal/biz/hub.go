package biz

import (
	"context"
	"log/slog"
	"math"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	accountv1 "github.com/Mars-08042/MarsChat/api/account/v1"
	v1 "github.com/Mars-08042/MarsChat/api/access/v1"
	"github.com/Mars-08042/MarsChat/app/access/internal/conf"
)

// 服务端关闭码，与 frame.proto 的说明一致。
const (
	CloseUnauthenticated = 4001
	CloseKicked          = 4002
	CloseSlowConsumer    = 4003
	CloseRateLimited     = 4008
)

const (
	// 发出关闭帧后等待对端回应的时间，超时直接断开 TCP。先等回应再断开，
	// 避免对端还有未读数据时直接关闭触发 RST，导致关闭前的 Kicked、Error 帧丢失
	closeGrace = 2 * time.Second
	// 在线路由与吊销名单的单次 Redis 操作超时
	redisTimeout = 2 * time.Second
)

// options 是连接参数，来自配置的 websocket 段，修改后需要重启。
type options struct {
	authTimeout       time.Duration
	heartbeatInterval time.Duration
	heartbeatTimeout  time.Duration
	writeTimeout      time.Duration
	sendQueue         int
	maxFrameBytes     int64
}

// Hub 管理本实例上的全部连接，同时是 Kratos 的 transport.Server：停机时以 1001 关闭全部连接并删除路由。
type Hub struct {
	opts   options
	policy *PolicyHolder
	auth   Authenticator
	routes RouteRepo
	sender MessageSender
	logger *slog.Logger

	mu       sync.Mutex
	all      map[*Conn]struct{} // 全部连接，包括尚未认证的
	byID     map[int64]*Conn    // 已认证的连接
	stopping bool
	wg       sync.WaitGroup // 正在服务的连接
	stopped  chan struct{}
}

// NewHub 创建连接管理器。
func NewHub(c *conf.WebSocket, policy *PolicyHolder, a Authenticator, routes RouteRepo, sender MessageSender, logger *slog.Logger) *Hub {
	return &Hub{
		opts: options{
			authTimeout:       c.GetAuthTimeout().AsDuration(),
			heartbeatInterval: c.GetHeartbeatInterval().AsDuration(),
			heartbeatTimeout:  c.GetHeartbeatTimeout().AsDuration(),
			writeTimeout:      c.GetWriteTimeout().AsDuration(),
			sendQueue:         int(c.GetSendQueue()),
			maxFrameBytes:     int64(c.GetMaxFrameBytes()),
		},
		policy:  policy,
		auth:    a,
		routes:  routes,
		sender:  sender,
		logger:  logger,
		all:     make(map[*Conn]struct{}),
		byID:    make(map[int64]*Conn),
		stopped: make(chan struct{}),
	}
}

// Serve 接管一条已完成握手的 WebSocket 连接，直到连接关闭才返回。
// 连接的生命周期与 HTTP 请求的 context 无关：Kratos 给请求 context 加了服务端超时。
func (h *Hub) Serve(ws *websocket.Conn) {
	c := newConn(h, ws)
	h.mu.Lock()
	if h.stopping {
		h.mu.Unlock()
		_ = ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseGoingAway, "server shutting down"), time.Now().Add(time.Second))
		_ = ws.Close()
		return
	}
	h.all[c] = struct{}{}
	h.wg.Add(1)
	h.mu.Unlock()
	defer h.wg.Done()

	c.serve()

	h.mu.Lock()
	delete(h.all, c)
	if id := c.id.Load(); id != 0 {
		delete(h.byID, id)
	}
	h.mu.Unlock()
}

// bind 给认证成功的连接分配 ID 并登记，之后 Push、Kick 才能找到它。
// ID 是 63 位随机数，在线路由的字段直接用它，不需要全局计数器；极小概率重复时重新生成。
func (h *Hub) bind(c *Conn) int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	for {
		id := rand.Int64N(math.MaxInt64) + 1
		if _, ok := h.byID[id]; !ok {
			c.id.Store(id)
			h.byID[id] = c
			return id
		}
	}
}

func (h *Hub) lookup(ids []int64) []*Conn {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*Conn, 0, len(ids))
	for _, id := range ids {
		if c, ok := h.byID[id]; ok {
			out = append(out, c)
		}
	}
	return out
}

// Push 把帧放进连接的发送队列，返回实际放入的连接数。不在本实例的连接忽略。
func (h *Hub) Push(connIDs []int64, frame *v1.ServerFrame) int {
	n := 0
	for _, c := range h.lookup(connIDs) {
		if c.push(frame) {
			n++
		}
	}
	return n
}

// Kick 发送 Kicked 帧后以 4002 关闭连接，返回找到的连接数。
func (h *Hub) Kick(connIDs []int64, reason accountv1.RevokeReason) int {
	conns := h.lookup(connIDs)
	frame := &v1.ServerFrame{Body: &v1.ServerFrame_Kicked{Kicked: &v1.Kicked{Reason: reason}}}
	for _, c := range conns {
		c.closeWith(frame, CloseKicked, "kicked")
	}
	return len(conns)
}

// Start 实现 transport.Server。连接由 HTTP 服务器接入，这里只等待停机。
func (h *Hub) Start(context.Context) error {
	<-h.stopped
	return nil
}

// Stop 以 1001 关闭全部连接，等待它们删除路由后返回。超过 ctx 期限时强制断开剩余连接。
// 先从 Consul 摘除、通知客户端分批重连在重构步骤 5 实现。
func (h *Hub) Stop(ctx context.Context) error {
	h.mu.Lock()
	if h.stopping {
		h.mu.Unlock()
		return nil
	}
	h.stopping = true
	conns := make([]*Conn, 0, len(h.all))
	for c := range h.all {
		conns = append(conns, c)
	}
	h.mu.Unlock()
	defer close(h.stopped)

	h.logger.Info("closing websocket connections", slog.Int("count", len(conns)))
	for _, c := range conns {
		c.closeWith(nil, websocket.CloseGoingAway, "server shutting down")
	}
	done := make(chan struct{})
	go func() {
		h.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		for _, c := range conns {
			_ = c.ws.Close()
		}
		h.logger.Warn("websocket connections did not close in time, forced", slog.Int("count", len(conns)))
		return ctx.Err()
	}
}
