// Package biz 实现投递：根据消息、已读、会话吊销、封禁和 social 事件确定接收连接，按 access 实例分组推送或踢连接。
// 推送尽力而为（ADR-0006）：任何失败都只记日志，不重试、不阻塞后续事件，客户端靠补拉兜底。
package biz

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/google/wire"

	accessv1 "github.com/Mars-08042/MarsChat/api/access/v1"
	accountv1 "github.com/Mars-08042/MarsChat/api/account/v1"
	"github.com/Mars-08042/MarsChat/app/delivery/internal/conf"
	"github.com/Mars-08042/MarsChat/pkg/route"
)

// ErrInstanceCoolingDown 表示目标 access 实例刚推送失败、仍在冷却期，这次调用被跳过。
var ErrInstanceCoolingDown = errors.New("access instance cooling down after a failed call")

// PolicyHolder 持有当前生效的策略。配置热更新时整体替换，读取方每次使用时取一次快照。
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

// ProviderSet 是 biz 层的依赖提供者。
var ProviderSet = wire.NewSet(NewDeliveryUsecase)

// RouteRepo 读取在线路由，见 pkg/route。
type RouteRepo interface {
	// List 返回各用户的在线连接，不在线的用户不出现在结果中。
	// 部分路由无法解析时同时返回结果和错误
	List(ctx context.Context, userIDs []int64) (map[int64][]route.Entry, error)
}

// Deduper 保证同一事件只推送一次。
type Deduper interface {
	// FirstSeen 占位并返回事件是否第一次出现
	FirstSeen(ctx context.Context, eventID string) (bool, error)
}

// GroupMember 是推送需要的群成员信息。
type GroupMember struct {
	UserID int64
	// Manager 表示群主或管理员，入群申请只通知他们
	Manager bool
}

// GroupMembers 查询群的全部成员。
type GroupMembers interface {
	List(ctx context.Context, groupID int64) ([]GroupMember, error)
}

// Pusher 调用 access 实例的内部接口，addr 为路由中记录的 gRPC 地址。
// 实例刚调用失败、仍在冷却期时返回 ErrInstanceCoolingDown。
type Pusher interface {
	Push(ctx context.Context, addr string, connIDs []int64, frame *accessv1.ServerFrame) (int, error)
	Kick(ctx context.Context, addr string, connIDs []int64, reason accountv1.RevokeReason) (int, error)
}
