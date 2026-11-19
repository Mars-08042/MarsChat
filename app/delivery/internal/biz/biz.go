// Package biz 实现投递：根据消息、已读、会话吊销和封禁事件确定接收连接，按 access 实例分组推送或踢连接。
// 推送尽力而为（ADR-0006）：任何失败都只记日志，不重试、不阻塞后续事件，客户端靠补拉兜底。
package biz

import (
	"context"

	"github.com/google/wire"

	accessv1 "github.com/Mars-08042/MarsChat/api/access/v1"
	accountv1 "github.com/Mars-08042/MarsChat/api/account/v1"
	"github.com/Mars-08042/MarsChat/pkg/route"
)

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

// GroupMembers 查询群的全部成员 ID。
type GroupMembers interface {
	List(ctx context.Context, groupID int64) ([]int64, error)
}

// Pusher 调用 access 实例的内部接口，addr 为路由中记录的 gRPC 地址。
type Pusher interface {
	Push(ctx context.Context, addr string, connIDs []int64, frame *accessv1.ServerFrame) (int, error)
	Kick(ctx context.Context, addr string, connIDs []int64, reason accountv1.RevokeReason) (int, error)
}
