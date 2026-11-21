package biz

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"strconv"
	"sync/atomic"
	"time"

	kerrors "github.com/go-kratos/kratos/v3/errors"
	"github.com/gorilla/websocket"

	accountv1 "github.com/Mars-08042/MarsChat/api/account/v1"
	v1 "github.com/Mars-08042/MarsChat/api/access/v1"
	messagev1 "github.com/Mars-08042/MarsChat/api/message/v1"
	"github.com/Mars-08042/MarsChat/pkg/auth"
)

// outbound 是发送队列中的一项。closeCode 非 0 时，写完 frame（可以为空）后以该关闭码关闭连接。
type outbound struct {
	frame     *v1.ServerFrame
	closeCode int
	closeText string
}

type closeInfo struct {
	code int
	text string
}

// Conn 是一条 WebSocket 连接。读协程（serve 所在的协程）按顺序处理上行帧，写协程独占发送；
// 其他协程（Push、Kick、停机）只通过发送队列和 closeWith 与连接交互。
type Conn struct {
	hub   *Hub
	ws    *websocket.Conn
	codec codec
	send  chan outbound
	quit  chan struct{} // 读协程退出时关闭，通知写协程结束
	// 已安排关闭时非空，之后的推送直接丢弃；同时记录关闭原因
	closed atomic.Pointer[closeInfo]
	// 认证成功后分配，0 表示尚未认证
	id atomic.Int64

	// 以下字段只由读协程访问
	principal   auth.Principal
	connectedAt time.Time
	windowStart time.Time
	windowCount int
	call        callRef // 参与的通话，见 Calls
}

func newConn(h *Hub, ws *websocket.Conn) *Conn {
	return &Conn{
		hub:         h,
		ws:          ws,
		codec:       codecFor(ws.Subprotocol()),
		send:        make(chan outbound, h.opts.sendQueue),
		quit:        make(chan struct{}),
		connectedAt: time.Now(),
	}
}

func (c *Conn) serve() {
	c.ws.SetReadLimit(c.hub.opts.maxFrameBytes)
	writerDone := make(chan struct{})
	go func() {
		c.writeLoop()
		close(writerDone)
	}()
	reason := c.readLoop()
	close(c.quit)
	<-writerDone

	id := c.id.Load()
	if ci := c.closed.Load(); ci != nil {
		reason = ci.text + " (" + strconv.Itoa(ci.code) + ")"
	}
	if id == 0 {
		c.hub.logger.Debug("unauthenticated websocket closed", slog.String("reason", reason))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), redisTimeout)
	defer cancel()
	if err := c.hub.routes.Unregister(ctx, c.principal.UserID, id); err != nil {
		// 删除失败时路由按 TTL 过期，期间推送到这条连接会被 access 忽略
		c.hub.logger.Warn("unregister route failed", slog.Int64("conn_id", id), slog.Any("error", err))
	}
	c.hub.calls.disconnect(c)
	c.hub.logger.Info("websocket disconnected",
		slog.Int64("conn_id", id),
		slog.Int64("user_id", c.principal.UserID),
		slog.String("reason", reason),
		slog.Duration("duration", time.Since(c.connectedAt)),
	)
}

// readLoop 读取并处理上行帧，返回连接结束的原因。认证前的读期限为认证超时，认证后每收到一帧就顺延一个心跳超时。
func (c *Conn) readLoop() string {
	deadline := time.Now().Add(c.hub.opts.authTimeout)
	for {
		_ = c.ws.SetReadDeadline(deadline)
		_, data, err := c.ws.ReadMessage()
		if err != nil {
			return c.onReadError(err)
		}
		if c.closed.Load() != nil {
			// 已安排关闭：丢弃后续帧，继续读到对端回应关闭帧为止
			continue
		}
		if !c.allowFrame() {
			c.hub.logger.Warn("websocket frame rate exceeded", slog.Int64("conn_id", c.id.Load()), slog.Int64("user_id", c.principal.UserID))
			c.closeWith(nil, CloseRateLimited, "too many frames")
			continue
		}
		f := &v1.ClientFrame{}
		decodeErr := c.codec.unmarshal(data, f)
		if c.id.Load() == 0 {
			c.handleAuth(f, decodeErr)
		} else {
			c.handle(f, decodeErr)
		}
		if c.id.Load() != 0 {
			deadline = time.Now().Add(c.hub.opts.heartbeatTimeout)
		}
	}
}

func (c *Conn) onReadError(err error) string {
	var ce *websocket.CloseError
	var ne net.Error
	switch {
	case errors.As(err, &ce):
		return "peer closed (" + strconv.Itoa(ce.Code) + ")"
	case errors.Is(err, websocket.ErrReadLimit):
		// gorilla 已经回复了 1009
		return "frame too large"
	case errors.As(err, &ne) && ne.Timeout():
		if c.id.Load() == 0 && c.closed.CompareAndSwap(nil, &closeInfo{CloseUnauthenticated, "authentication timeout"}) {
			_ = c.ws.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(CloseUnauthenticated, "authentication timeout"), time.Now().Add(time.Second))
		}
		// 心跳超时：对端多半已经失联，不再发关闭帧，直接断开
		return "heartbeat timeout"
	default:
		return "read error: " + err.Error()
	}
}

// allowFrame 按固定 1 秒窗口统计上行帧数。只用进程内计数，作用是保护 access 自身。
func (c *Conn) allowFrame() bool {
	now := time.Now()
	if now.Sub(c.windowStart) >= time.Second {
		c.windowStart = now
		c.windowCount = 0
	}
	c.windowCount++
	return c.windowCount <= int(c.hub.policy.Load().GetFramesPerSecond())
}

func (c *Conn) handleAuth(f *v1.ClientFrame, decodeErr error) {
	a := f.GetAuth()
	if decodeErr != nil || a == nil {
		c.closeWith(errorFrame(f.GetRequestId(), ErrInvalidFrame("first frame must be auth")), CloseUnauthenticated, "unauthenticated")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), redisTimeout)
	defer cancel()
	p, err := c.hub.auth.Authenticate(ctx, a.GetAccessToken())
	if err != nil {
		c.hub.logger.Info("websocket authentication failed", slog.String("reason", kerrors.FromError(err).Message))
		c.closeWith(errorFrame(f.GetRequestId(), err), CloseUnauthenticated, "unauthenticated")
		return
	}
	c.principal = p
	// 顺序：登记到 Hub → 回复 AuthOk → 写在线路由。delivery 只能经路由找到连接，所以推送不会早于 AuthOk
	id := c.hub.bind(c)
	c.push(&v1.ServerFrame{RequestId: f.GetRequestId(), Body: &v1.ServerFrame_AuthOk{AuthOk: &v1.AuthOk{
		UserId:                   p.UserID,
		SessionId:                p.SessionID,
		ConnectionId:             id,
		HeartbeatIntervalSeconds: int32(c.hub.opts.heartbeatInterval / time.Second),
	}}})
	c.registerRoute(ctx)
	c.hub.logger.Info("websocket connected",
		slog.Int64("conn_id", id),
		slog.Int64("user_id", p.UserID),
		slog.Int64("session_id", p.SessionID),
	)
}

func (c *Conn) handle(f *v1.ClientFrame, decodeErr error) {
	rid := f.GetRequestId()
	if decodeErr != nil {
		c.push(errorFrame(rid, ErrInvalidFrame("cannot decode frame")))
		return
	}
	switch b := f.GetBody().(type) {
	case *v1.ClientFrame_Ping:
		c.heartbeat(rid)
	case *v1.ClientFrame_SendMessage:
		c.sendMessage(rid, b.SendMessage)
	case *v1.ClientFrame_Call:
		c.hub.calls.handle(c, rid, b.Call)
	case *v1.ClientFrame_Auth:
		c.push(errorFrame(rid, ErrInvalidFrame("already authenticated")))
	default:
		c.push(errorFrame(rid, ErrInvalidFrame("empty frame")))
	}
}

// heartbeat 回复 Pong，同时续期在线路由和参与的通话、复查吊销名单。事件链路故障时，被吊销的连接最迟一个心跳周期后断开。
func (c *Conn) heartbeat(rid uint64) {
	ctx, cancel := context.WithTimeout(context.Background(), redisTimeout)
	defer cancel()
	revoked, err := c.hub.auth.IsRevoked(ctx, c.principal)
	if err != nil {
		// 与 HTTP 接口一致：Redis 故障时放行
		c.hub.logger.Warn("revocation check failed, keeping connection", slog.Int64("conn_id", c.id.Load()), slog.Any("error", err))
	} else if revoked {
		c.hub.logger.Info("token revoked, closing connection", slog.Int64("conn_id", c.id.Load()), slog.Int64("user_id", c.principal.UserID))
		c.closeWith(&v1.ServerFrame{Body: &v1.ServerFrame_Kicked{Kicked: &v1.Kicked{Reason: accountv1.RevokeReason_REVOKE_REASON_UNSPECIFIED}}},
			CloseKicked, "token revoked")
		return
	}
	c.registerRoute(ctx)
	c.hub.calls.heartbeat(ctx, c)
	c.push(&v1.ServerFrame{RequestId: rid, Body: &v1.ServerFrame_Pong{Pong: &v1.Pong{}}})
}

// registerRoute 登记或续期路由。失败时只记日志不断开：连接仍能收发，下次心跳会重试。
func (c *Conn) registerRoute(ctx context.Context) {
	id := c.id.Load()
	if err := c.hub.routes.Register(ctx, c.principal.UserID, id, c.principal.SessionID, c.hub.opts.heartbeatTimeout); err != nil {
		c.hub.logger.Warn("register route failed", slog.Int64("conn_id", id), slog.Any("error", err))
	}
}

// sendMessage 同步调用 message：读协程等待结果后才读下一帧，同一连接内的确认与发送顺序一致。
func (c *Conn) sendMessage(rid uint64, m *messagev1.OutgoingMessage) {
	res, err := c.hub.sender.Send(context.Background(), c.principal, c.id.Load(), m)
	if err != nil {
		if e := kerrors.FromError(err); e.Code >= 500 {
			c.hub.logger.Warn("send message failed", slog.Int64("conn_id", c.id.Load()), slog.Any("error", err))
		}
		c.push(errorFrame(rid, err))
		return
	}
	c.push(&v1.ServerFrame{RequestId: rid, Body: &v1.ServerFrame_SendAck{SendAck: res}})
}

// push 把帧放进发送队列，不阻塞。队列已满说明客户端读得太慢，以 4003 断开，客户端重连后补拉。
func (c *Conn) push(f *v1.ServerFrame) bool {
	if c.closed.Load() != nil {
		return false
	}
	select {
	case c.send <- outbound{frame: f}:
		return true
	default:
		if c.closed.CompareAndSwap(nil, &closeInfo{CloseSlowConsumer, "send queue full"}) {
			c.hub.logger.Warn("send queue full, closing slow connection", slog.Int64("conn_id", c.id.Load()))
			go c.closeNow(CloseSlowConsumer, "send queue full")
		}
		return false
	}
}

// closeWith 安排关闭：写协程先发完队列中已有的帧和 frame，再发关闭帧。重复调用只有第一次生效。
func (c *Conn) closeWith(f *v1.ServerFrame, code int, text string) {
	if !c.closed.CompareAndSwap(nil, &closeInfo{code, text}) {
		return
	}
	select {
	case c.send <- outbound{frame: f, closeCode: code, closeText: text}:
	default:
		go c.closeNow(code, text)
	}
}

// closeNow 不经发送队列直接关闭，用于队列已满的连接。写协程可能正阻塞在写操作上，关闭帧尽力发送。
func (c *Conn) closeNow(code int, text string) {
	_ = c.ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, text), time.Now().Add(time.Second))
	_ = c.ws.Close()
}

func (c *Conn) writeLoop() {
	defer func() { _ = c.ws.Close() }()
	for {
		select {
		case out := <-c.send:
			if out.frame != nil {
				if err := c.write(out.frame); err != nil {
					return
				}
			}
			if out.closeCode != 0 {
				_ = c.ws.WriteControl(websocket.CloseMessage,
					websocket.FormatCloseMessage(out.closeCode, out.closeText), time.Now().Add(c.hub.opts.writeTimeout))
				select {
				case <-c.quit:
				case <-time.After(closeGrace):
				}
				return
			}
		case <-c.quit:
			return
		}
	}
}

func (c *Conn) write(f *v1.ServerFrame) error {
	data, err := c.codec.marshal(f)
	if err != nil {
		c.hub.logger.Error("encode frame failed", slog.Int64("conn_id", c.id.Load()), slog.Any("error", err))
		return nil
	}
	_ = c.ws.SetWriteDeadline(time.Now().Add(c.hub.opts.writeTimeout))
	return c.ws.WriteMessage(c.codec.msgType, data)
}
