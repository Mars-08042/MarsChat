// Package biz 是 message 服务的业务逻辑：会话、发送、历史与补拉、已读，以及由 social 事件维护的群成员。
// 存储细节由 data 层实现这里定义的接口。
package biz

import (
	"context"
	"errors"
	"time"

	"github.com/google/wire"
	"google.golang.org/protobuf/proto"

	accountv1 "github.com/Mars-08042/MarsChat/api/account/v1"
	v1 "github.com/Mars-08042/MarsChat/api/message/v1"
	"github.com/Mars-08042/MarsChat/pkg/kafka"
	"github.com/Mars-08042/MarsChat/pkg/ratelimit"
)

// ProviderSet 是 biz 层的依赖提供者。
var ProviderSet = wire.NewSet(
	NewMessageUsecase,
	NewConversationUsecase,
	NewMembershipUsecase,
)

// TopicMessageEvents 是 message 事件的 Kafka topic，分区键为会话 ID。
const TopicMessageEvents = kafka.TopicMessageEvents

// 数据层返回的通用错误，由用例转换成对外的业务错误。
var (
	ErrNotFound = errors.New("not found")
	// ErrDuplicate 表示违反唯一约束：同一发送者在同一会话中重复使用了 client_msg_id
	ErrDuplicate = errors.New("duplicate")
	// ErrUnavailable 表示依赖的服务不可用（与业务拒绝区分）
	ErrUnavailable = errors.New("dependency unavailable")
)

// 会话类型，与 message.v1.ConversationType、social.v1.PeerType 的数值一致。
const (
	TypeSingle int32 = 1
	TypeGroup  int32 = 2
)

// Conversation 是会话。群聊的 ID 等于群 ID；单聊的 UserLow、UserHigh 为两人中较小、较大的 ID。
type Conversation struct {
	ID        int64
	Type      int32
	UserLow   int64
	UserHigh  int64
	MaxSeq    int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Peer 返回单聊中 me 的对方。
func (c *Conversation) Peer(me int64) int64 {
	if c.UserLow == me {
		return c.UserHigh
	}
	return c.UserLow
}

// Member 是会话成员与已读位置。
type Member struct {
	ConversationID int64
	UserID         int64
	ReadSeq        int64
	JoinedAt       time.Time
	UpdatedAt      time.Time
}

// Message 是一条消息，(ConversationID, Seq) 唯一。
type Message struct {
	ConversationID int64
	Seq            int64
	SenderID       int64
	ClientMsgID    string
	Content        *v1.MessageContent
	CreatedAt      time.Time
}

// SeqKey 定位一条消息。
type SeqKey struct {
	ConversationID int64
	Seq            int64
}

// GroupBrief 是会话列表需要的群资料。
type GroupBrief struct {
	ID          int64
	Name        string
	Avatar      string
	Status      int32
	MemberCount int32
}

// Transaction 在一个数据库事务中执行 fn，fn 内通过 ctx 使用同一事务。
// 各用例的加锁顺序一致（先会话行、再成员行和消息），不做死锁重试；万一死锁，错误返回给客户端，
// 发送由客户端用同一个 client_msg_id 重发，结果仍然幂等。
type Transaction interface {
	InTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// ConversationRepo 存取会话。
type ConversationRepo interface {
	Get(ctx context.Context, id int64) (*Conversation, error)
	FindPair(ctx context.Context, low, high int64) (*Conversation, error)
	BatchGet(ctx context.Context, ids []int64) (map[int64]*Conversation, error)
	// CreateIfAbsent 插入会话，已存在（主键或单聊的 uk_pair 冲突）时不做任何修改。
	// 用 INSERT ... ON DUPLICATE KEY UPDATE 实现：重复时加排他锁，不会像 INSERT IGNORE 那样加共享锁后再升级而死锁。
	CreateIfAbsent(ctx context.Context, c *Conversation) error
	// Lock、LockPair 用 SELECT ... FOR UPDATE 锁住会话行，必须在事务中调用，会话必须已存在。
	Lock(ctx context.Context, id int64) (*Conversation, error)
	LockPair(ctx context.Context, low, high int64) (*Conversation, error)
	SetMaxSeq(ctx context.Context, id, seq int64, now time.Time) error
}

// MemberRepo 存取会话成员。
type MemberRepo interface {
	Get(ctx context.Context, conversationID, userID int64) (*Member, error)
	// AddIgnore 插入成员，已存在的忽略。
	AddIgnore(ctx context.Context, members []*Member) error
	// AddAtLatest 插入成员，已读位置取会话当前的 max_seq（入群前的历史不计入未读），已存在的忽略。
	AddAtLatest(ctx context.Context, conversationID int64, userIDs []int64, now time.Time) error
	Remove(ctx context.Context, conversationID, userID int64) error
	// ListByUser 按会话 ID 升序列出用户的会话，从 cursor 之后开始。
	ListByUser(ctx context.Context, userID, cursor int64, limit int) ([]*Member, error)
	// ListOthers 返回这些会话中除 me 以外的成员，按会话 ID 分组。只用于单聊取对方的已读位置。
	ListOthers(ctx context.Context, conversationIDs []int64, me int64) (map[int64]*Member, error)
	// AdvanceRead 把已读位置推进到 seq，原位置不小于 seq 时不修改。返回是否修改了。
	AdvanceRead(ctx context.Context, conversationID, userID, seq int64, now time.Time) (bool, error)
}

// MessageRepo 存取消息。
type MessageRepo interface {
	// Create 插入消息，违反 uk_client 时返回 ErrDuplicate。
	Create(ctx context.Context, m *Message) error
	FindByClientID(ctx context.Context, conversationID, senderID int64, clientMsgID string) (*Message, error)
	// ListBefore 返回 seq 小于 beforeSeq 的消息，按 seq 降序。
	ListBefore(ctx context.Context, conversationID, beforeSeq int64, limit int) ([]*Message, error)
	// ListAfter 返回 seq 大于 afterSeq 的消息，按 seq 升序。
	ListAfter(ctx context.Context, conversationID, afterSeq int64, limit int) ([]*Message, error)
	BatchGet(ctx context.Context, keys []SeqKey) (map[SeqKey]*Message, error)
}

// EventRepo 在当前事务中写入 Outbox 事件。
type EventRepo interface {
	Add(ctx context.Context, topic, key string, msg proto.Message, now time.Time) error
}

// PermissionChecker 校验发言权限（social CheckSendPermission）。不允许时原样返回 social 的业务错误；
// social 不可用时返回包装了 ErrUnavailable 的错误。
type PermissionChecker interface {
	CheckSend(ctx context.Context, senderID int64, peerType int32, peerID int64) error
}

// UserService 批量获取用户资料（account）。
type UserService interface {
	BatchGetUsers(ctx context.Context, ids []int64) (map[int64]*accountv1.UserProfile, error)
}

// GroupService 批量获取群资料（social）。
type GroupService interface {
	BatchGetGroups(ctx context.Context, ids []int64) (map[int64]*GroupBrief, error)
}

// RateLimiter 是滑动窗口限流器。
type RateLimiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (ratelimit.Result, error)
}

// IDGenerator 生成 snowflake ID。
type IDGenerator interface {
	NextID() (int64, error)
}
