// Package biz 是 social 服务的业务逻辑：好友、拉黑、群与发言权限。
// 存储细节由 data 层实现这里定义的接口。
package biz

import (
	"context"
	"errors"
	"time"

	"github.com/google/wire"
	"google.golang.org/protobuf/proto"

	"github.com/Mars-08042/MarsChat/pkg/kafka"
	"github.com/Mars-08042/MarsChat/pkg/ratelimit"
)

// ProviderSet 是 biz 层的依赖提供者。
var ProviderSet = wire.NewSet(
	NewProfileLoader,
	NewFriendUsecase,
	NewGroupUsecase,
	NewPermissionUsecase,
)

// TopicSocialEvents 是 social 事件的 Kafka topic。
const TopicSocialEvents = kafka.TopicSocialEvents

// 数据层返回的通用错误，由用例转换成对外的业务错误。
var (
	ErrNotFound = errors.New("not found")
	// ErrDuplicate 表示违反唯一约束，例如并发创建了同一对用户的待处理申请
	ErrDuplicate = errors.New("duplicate")
	// ErrUnavailable 表示依赖的服务不可用
	ErrUnavailable = errors.New("dependency unavailable")
)

// 取值与 social.v1 中的枚举一致，数据库直接存这些数值。
const (
	RequestPending  int32 = 1
	RequestAccepted int32 = 2
	RequestRejected int32 = 3
	RequestExpired  int32 = 4

	JoinModeFree     int32 = 1
	JoinModeApproval int32 = 2

	RoleMember int32 = 1
	RoleAdmin  int32 = 2
	RoleOwner  int32 = 3

	GroupNormal    int32 = 1
	GroupDismissed int32 = 2

	LeaveReasonLeft    int32 = 1
	LeaveReasonRemoved int32 = 2
)

// userStatusBanned 与 account.v1.UserStatus 的封禁取值一致。
const userStatusBanned int32 = 2

// FriendRequest 是好友申请。
type FriendRequest struct {
	ID         int64
	FromUserID int64
	ToUserID   int64
	Message    string
	Status     int32
	CreatedAt  time.Time
	UpdatedAt  time.Time
	HandledAt  *time.Time
}

// Friend 是一条好友关系（UserID 一侧）。
type Friend struct {
	UserID    int64
	FriendID  int64
	CreatedAt time.Time
}

// Block 是一条拉黑关系。
type Block struct {
	UserID    int64
	BlockedID int64
	CreatedAt time.Time
}

// Group 是群资料。
type Group struct {
	ID          int64
	Name        string
	Avatar      string
	Notice      string
	OwnerID     int64
	JoinMode    int32
	MuteAll     bool
	MemberCount int32
	Status      int32
	Version     int32
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DismissedAt *time.Time
}

// GroupUpdate 是资料修改，nil 表示不修改该字段。
type GroupUpdate struct {
	Name     *string
	Avatar   *string
	Notice   *string
	JoinMode *int32
	MuteAll  *bool
}

func (u GroupUpdate) empty() bool {
	return u.Name == nil && u.Avatar == nil && u.Notice == nil && u.JoinMode == nil && u.MuteAll == nil
}

// Member 是群成员。
type Member struct {
	GroupID    int64
	UserID     int64
	Role       int32
	MutedUntil *time.Time
	InviterID  int64
	JoinedAt   time.Time
}

// MutedAt 判断在 now 时是否处于单人禁言中。
func (m *Member) MutedAt(now time.Time) bool {
	return m.MutedUntil != nil && m.MutedUntil.After(now)
}

// JoinRequest 是入群申请。
type JoinRequest struct {
	ID        int64
	GroupID   int64
	UserID    int64
	Message   string
	Status    int32
	HandledBy int64
	CreatedAt time.Time
	UpdatedAt time.Time
	HandledAt *time.Time
}

// MemberGroup 是“我的群”列表中的一项：群资料加本人角色。
type MemberGroup struct {
	Group *Group
	Role  int32
}

// UserProfile 是从 account 取得的公开资料。
type UserProfile struct {
	ID        int64
	Nickname  string
	Avatar    string
	Gender    int32
	Signature string
	Status    int32
}

// Transaction 在一个数据库事务中执行 fn；fn 内的 repo 调用通过 ctx 使用同一个事务。
type Transaction interface {
	InTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// FriendRepo 是好友关系存储。一段关系存两行，由 AddPair、DeletePair 同时维护。
type FriendRepo interface {
	AreFriends(ctx context.Context, userID, friendID int64) (bool, error)
	// AddPair 插入两行关系，已存在时忽略，返回是否新建了关系
	AddPair(ctx context.Context, a, b int64, now time.Time) (bool, error)
	// DeletePair 删除两行关系，返回是否删除了
	DeletePair(ctx context.Context, a, b int64) (bool, error)
	// FilterFriends 返回 ids 中是 userID 好友的那些
	FilterFriends(ctx context.Context, userID int64, ids []int64) ([]int64, error)
	// List 按 friend_id 递增返回 friend_id > after 的最多 limit 条
	List(ctx context.Context, userID, after int64, limit int) ([]*Friend, error)
}

// FriendRequestRepo 是好友申请存储。
type FriendRequestRepo interface {
	// Create 新建申请，同一对用户已有待处理申请时返回 ErrDuplicate
	Create(ctx context.Context, r *FriendRequest) error
	FindByID(ctx context.Context, id int64) (*FriendRequest, error)
	// FindPending 返回 from 发给 to 的待处理申请，没有时返回 ErrNotFound
	FindPending(ctx context.Context, from, to int64) (*FriendRequest, error)
	// Refresh 更新待处理申请的附言和更新时间
	Refresh(ctx context.Context, id int64, message string, now time.Time) error
	// Handle 在申请仍为待处理、且接收方为 toUserID 时改为 status，返回是否修改成功
	Handle(ctx context.Context, id, toUserID int64, status int32, now time.Time) (bool, error)
	// ResolvePending 把 from 发给 to 的待处理申请（如果有）改为 status
	ResolvePending(ctx context.Context, from, to int64, status int32, now time.Time) error
	// ListReceived 按 id 递减返回发给 toUserID 的待处理申请，before 为 0 表示从最新开始
	ListReceived(ctx context.Context, toUserID, before int64, limit int) ([]*FriendRequest, error)
	// ListSent 按 id 递减返回 fromUserID 发出的全部申请
	ListSent(ctx context.Context, fromUserID, before int64, limit int) ([]*FriendRequest, error)
}

// BlockRepo 是拉黑存储。
type BlockRepo interface {
	// Add 拉黑，已拉黑时忽略
	Add(ctx context.Context, userID, blockedID int64, now time.Time) error
	Remove(ctx context.Context, userID, blockedID int64) error
	// EitherBlocked 判断两人之间是否有任何一方拉黑了另一方
	EitherBlocked(ctx context.Context, a, b int64) (bool, error)
	// List 按 blocked_id 递增返回 blocked_id > after 的最多 limit 条
	List(ctx context.Context, userID, after int64, limit int) ([]*Block, error)
}

// GroupRepo 是群资料存储。Find* 直接查数据库，Get* 经过缓存。
type GroupRepo interface {
	Create(ctx context.Context, g *Group) error
	FindByID(ctx context.Context, id int64) (*Group, error)
	// Lock 在事务中锁住群行（SELECT ... FOR UPDATE）并返回最新资料，不存在时返回 ErrNotFound
	Lock(ctx context.Context, id int64) (*Group, error)
	// UpdateProfile 在群正常且版本号为 version 时修改资料并把版本号加一，返回是否修改成功
	UpdateProfile(ctx context.Context, id int64, version int32, upd GroupUpdate, now time.Time) (bool, error)
	AddMemberCount(ctx context.Context, id int64, delta int) error
	// SetOwner 修改群主并把版本号加一
	SetOwner(ctx context.Context, id, ownerID int64, now time.Time) error
	// Dismiss 把群标记为已解散，成员数清零并把版本号加一
	Dismiss(ctx context.Context, id int64, now time.Time) error

	GetCached(ctx context.Context, id int64) (*Group, error)
	// BatchGetCached 按输入顺序返回存在的群
	BatchGetCached(ctx context.Context, ids []int64) ([]*Group, error)
	// EvictCache 在数据库提交后删除群资料缓存，失败只记日志（靠 TTL 兜底）
	EvictCache(ctx context.Context, id int64)
	// OnCreated 在建群提交后调用，把群 ID 加入布隆过滤器
	OnCreated(ctx context.Context, id int64)
}

// GroupCacheInvalidator 删除群资料和成员列表缓存，失败时返回错误。
// 事件消费者用它兜底同步删除（TD-25）：同步删除失败时不必等 TTL 过期，消费者会重试到成功为止。
type GroupCacheInvalidator interface {
	InvalidateGroup(ctx context.Context, groupID int64) error
}

// MemberRepo 是群成员存储。
type MemberRepo interface {
	// Find 返回成员，不是成员时返回 ErrNotFound
	Find(ctx context.Context, groupID, userID int64) (*Member, error)
	// FilterMembers 返回 userIDs 中已是成员的那些
	FilterMembers(ctx context.Context, groupID int64, userIDs []int64) ([]int64, error)
	Add(ctx context.Context, members []*Member) error
	// Remove 删除成员，返回是否删除了
	Remove(ctx context.Context, groupID, userID int64) (bool, error)
	SetRole(ctx context.Context, groupID, userID int64, role int32) error
	SetMutedUntil(ctx context.Context, groupID, userID int64, until *time.Time) error
	// RemoveAll 删除全部成员，返回被删除的成员 ID
	RemoveAll(ctx context.Context, groupID int64) ([]int64, error)
	// List 按 user_id 递增返回 user_id > after 的最多 limit 个成员
	List(ctx context.Context, groupID, after int64, limit int) ([]*Member, error)
	// ListByUser 按 group_id 递增返回用户加入的群（group_id > after）中的成员记录
	ListByUser(ctx context.Context, userID, after int64, limit int) ([]*Member, error)

	// GetAllCached 返回全部成员（只有 UserID、Role、MutedUntil），走成员列表缓存
	GetAllCached(ctx context.Context, groupID int64) ([]*Member, error)
	// EvictCache 在数据库提交后删除成员列表缓存，失败只记日志（靠 TTL 兜底）
	EvictCache(ctx context.Context, groupID int64)
}

// JoinRequestRepo 是入群申请存储。
type JoinRequestRepo interface {
	Create(ctx context.Context, r *JoinRequest) error
	FindByID(ctx context.Context, id int64) (*JoinRequest, error)
	FindPending(ctx context.Context, groupID, userID int64) (*JoinRequest, error)
	Refresh(ctx context.Context, id int64, message string, now time.Time) error
	// Handle 在申请仍为待处理时改为 status，返回是否修改成功
	Handle(ctx context.Context, id int64, status int32, handledBy int64, now time.Time) (bool, error)
	// ExpireForUsers 把这些用户对该群的待处理申请标记为已失效
	ExpireForUsers(ctx context.Context, groupID int64, userIDs []int64, now time.Time) error
	// ExpireAll 把该群全部待处理申请标记为已失效
	ExpireAll(ctx context.Context, groupID int64, now time.Time) error
	// ListPending 按 id 递减返回该群的待处理申请，before 为 0 表示从最新开始
	ListPending(ctx context.Context, groupID, before int64, limit int) ([]*JoinRequest, error)
}

// EventRepo 在当前事务中写入 Outbox 事件。
type EventRepo interface {
	Add(ctx context.Context, topic, key string, msg proto.Message, now time.Time) error
}

// UserService 是 account 服务提供的用户资料查询。
type UserService interface {
	// BatchGetUsers 返回存在的用户（含已封禁），account 不可用时返回包装了 ErrUnavailable 的错误
	BatchGetUsers(ctx context.Context, ids []int64) (map[int64]*UserProfile, error)
}

// RateLimiter 是按键的滑动窗口限流。
type RateLimiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (ratelimit.Result, error)
}

// IDGenerator 生成全局唯一 ID。
type IDGenerator interface {
	NextID() (int64, error)
}
