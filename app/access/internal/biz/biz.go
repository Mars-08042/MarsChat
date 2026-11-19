// Package biz 实现 access 的连接管理：首帧认证、心跳、收发与保护、在线路由登记，
// 以及 delivery 调用的推送和踢连接。access 的业务就是长连接本身，所以这里直接使用 gorilla/websocket 的连接。
package biz

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/google/wire"

	messagev1 "github.com/Mars-08042/MarsChat/api/message/v1"
	"github.com/Mars-08042/MarsChat/app/access/internal/conf"
	"github.com/Mars-08042/MarsChat/pkg/auth"
)

// ProviderSet 是 biz 层的依赖提供者。
var ProviderSet = wire.NewSet(NewHub)

// Authenticator 校验访问令牌，由 pkg/auth.Verifier 实现。
type Authenticator interface {
	// Authenticate 校验签名、声明和吊销名单，失败时返回 401 错误
	Authenticate(ctx context.Context, token string) (auth.Principal, error)
	// IsRevoked 复查令牌是否已被吊销，返回的错误表示无法确认（Redis 不可用）
	IsRevoked(ctx context.Context, p auth.Principal) (bool, error)
}

// RouteRepo 维护在线路由，见 pkg/route。
type RouteRepo interface {
	// Register 登记或续期连接的路由，ttl 后自动过期
	Register(ctx context.Context, userID, connID, sessionID int64, ttl time.Duration) error
	Unregister(ctx context.Context, userID, connID int64) error
}

// MessageSender 把客户端发送的消息转给 message 服务。
// 返回的错误是 Kratos 错误：message 的业务错误原样返回，调用失败时为 503 UPSTREAM_UNAVAILABLE。
type MessageSender interface {
	Send(ctx context.Context, p auth.Principal, connID int64, m *messagev1.OutgoingMessage) (*messagev1.SendResult, error)
}

// PolicyHolder 持有当前生效的限额策略。配置热更新时整体替换，读取方每次使用时取一次快照。
type PolicyHolder struct {
	v atomic.Pointer[conf.Policy]
}

// NewPolicyHolder 用启动时的配置初始化。
func NewPolicyHolder(p *conf.Policy) *PolicyHolder {
	h := &PolicyHolder{}
	h.v.Store(p)
	return h
}

// Load 返回当前策略，调用方不能修改返回值。
func (h *PolicyHolder) Load() *conf.Policy {
	return h.v.Load()
}

// Store 替换策略，由配置热更新调用（新值已通过校验）。
func (h *PolicyHolder) Store(p *conf.Policy) {
	h.v.Store(p)
}
