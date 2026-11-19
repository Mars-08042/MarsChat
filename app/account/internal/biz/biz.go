// Package biz 是 account 服务的业务逻辑：注册、登录、令牌、资料、会话与封禁。
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
	NewTokenService,
	NewSmsUsecase,
	NewAuthUsecase,
	NewUserUsecase,
	NewSessionUsecase,
	NewAdminUsecase,
)

// TopicAccountEvents 是 account 事件的 Kafka topic，分区键为用户 ID。
const TopicAccountEvents = kafka.TopicAccountEvents

// 数据层返回的通用错误，由用例转换成对外的业务错误。
var (
	ErrNotFound   = errors.New("not found")
	ErrPhoneTaken = errors.New("phone already registered")
)

// 取值与 account.v1 中的枚举一致，数据库直接存这些数值。
const (
	RoleUser  int32 = 1
	RoleAdmin int32 = 2

	StatusNormal int32 = 1
	StatusBanned int32 = 2
)

// User 是用户。从缓存读取时不包含 PasswordHash。
type User struct {
	ID           int64
	Phone        string
	PasswordHash string
	Nickname     string
	Avatar       string
	Gender       int32
	Birthday     *time.Time
	Signature    string
	Role         int32
	Status       int32
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// ProfileUpdate 是资料修改，nil 表示不修改该字段。
type ProfileUpdate struct {
	Nickname  *string
	Avatar    *string
	Gender    *int32
	Signature *string
	// SetBirthday 为 true 时修改生日，此时 Birthday 为 nil 表示清除
	SetBirthday bool
	Birthday    *time.Time
}

// Device 是发起登录的设备。
type Device struct {
	ID       string
	Platform int32
	Name     string
}

// Session 是一台设备的一次登录。
type Session struct {
	ID               int64
	UserID           int64
	Device           Device
	RefreshHash      []byte
	PrevRefreshHash  []byte
	RotatedAt        *time.Time
	RefreshExpiresAt time.Time
	LoginIP          string
	LastIP           string
	LastActiveAt     time.Time
	RevokedAt        *time.Time
	RevokeReason     int32
	CreatedAt        time.Time
}

// Transaction 在一个数据库事务中执行 fn；fn 内的 repo 调用通过 ctx 使用同一个事务。
type Transaction interface {
	InTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// UserRepo 是用户存储。Find* 直接查数据库，Get* 经过缓存。
type UserRepo interface {
	// Create 创建用户，手机号已存在时返回 ErrPhoneTaken
	Create(ctx context.Context, u *User) error
	FindByID(ctx context.Context, id int64) (*User, error)
	FindByPhone(ctx context.Context, phone string) (*User, error)
	ExistsByPhone(ctx context.Context, phone string) (bool, error)
	UpdateProfile(ctx context.Context, id int64, upd ProfileUpdate, now time.Time) error
	// SetStatus 修改状态，返回是否真的发生了变化
	SetStatus(ctx context.Context, id int64, status int32, now time.Time) (bool, error)
	// LockStatus 在事务中对用户行加排他锁（SELECT ... FOR UPDATE）并返回状态，用户不存在时返回 ErrNotFound
	LockStatus(ctx context.Context, id int64) (int32, error)

	GetCached(ctx context.Context, id int64) (*User, error)
	BatchGetCached(ctx context.Context, ids []int64) ([]*User, error)
	// EvictCache 在数据库提交后删除资料缓存，失败只记日志（靠 TTL 兜底）
	EvictCache(ctx context.Context, id int64)
	// OnRegistered 在注册提交后调用，把用户 ID 加入布隆过滤器
	OnRegistered(ctx context.Context, id int64)
}

// SessionRepo 是会话存储。
type SessionRepo interface {
	Create(ctx context.Context, s *Session) error
	FindByID(ctx context.Context, id int64) (*Session, error)
	// RevokeActiveByDevice 吊销该设备当前有效的会话，返回被吊销的会话 ID，没有时返回 0
	RevokeActiveByDevice(ctx context.Context, userID int64, deviceID string, reason int32, at time.Time) (int64, error)
	// Revoke 吊销属于 userID 的有效会话；会话不存在、不属于该用户或已吊销时返回 false
	Revoke(ctx context.Context, id, userID int64, reason int32, at time.Time) (bool, error)
	// RevokeAllByUser 吊销用户全部有效会话，返回被吊销的会话 ID
	RevokeAllByUser(ctx context.Context, userID int64, reason int32, at time.Time) ([]int64, error)
	// Rotate 在当前刷新令牌哈希仍为 oldHash 时原子替换为 newHash，否则返回 false
	Rotate(ctx context.Context, id int64, oldHash, newHash []byte, expiresAt time.Time, ip string, at time.Time) (bool, error)
	ListActive(ctx context.Context, userID int64) ([]*Session, error)
}

// EventRepo 在当前事务中写入 Outbox 事件。
type EventRepo interface {
	Add(ctx context.Context, topic, key string, msg proto.Message, now time.Time) error
}

// SlotResult 是占用短信发送名额的结果。
type SlotResult int

const (
	SlotOK SlotResult = iota
	SlotTooFrequent
	SlotDailyLimit
)

// VerifyResult 是验证码校验结果。
type VerifyResult int

const (
	VerifyOK VerifyResult = iota
	// VerifyMissing 表示没有验证码或已过期
	VerifyMissing
	VerifyMismatch
	// VerifyExhausted 表示错误次数达到上限，验证码已作废
	VerifyExhausted
)

// SmsCodeStore 保存验证码和发送计数。
type SmsCodeStore interface {
	// AcquireSendSlot 检查发送间隔和当日上限，通过时设置冷却并计数；因发送间隔被拒绝时返回剩余冷却时间
	AcquireSendSlot(ctx context.Context, phone, day string, interval time.Duration, dailyLimit int) (SlotResult, time.Duration, error)
	// ReleaseSendSlot 撤销一次占用（短信发送失败时）
	ReleaseSendSlot(ctx context.Context, phone, day string) error
	SaveCode(ctx context.Context, purpose int32, phone string, codeHash string, ttl time.Duration) error
	DeleteCode(ctx context.Context, purpose int32, phone string) error
	// VerifyCode 原子地比对并累计错误次数，成功或作废时删除验证码
	VerifyCode(ctx context.Context, purpose int32, phone string, codeHash string, maxAttempts int) (VerifyResult, error)
}

// LoginGuard 记录密码错误次数，防止暴力破解。
type LoginGuard interface {
	// Locked 判断是否处于锁定状态，返回剩余锁定时间
	Locked(ctx context.Context, phone string, maxFailures int) (bool, time.Duration, error)
	RecordFailure(ctx context.Context, phone string, maxFailures int, window, lock time.Duration) error
	Reset(ctx context.Context, phone string) error
}

// RateLimiter 是按键的滑动窗口限流。
type RateLimiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (ratelimit.Result, error)
}

// SmsSender 发送短信验证码。
type SmsSender interface {
	Send(ctx context.Context, phone, code string, purpose int32) error
}

// IDGenerator 生成全局唯一 ID。
type IDGenerator interface {
	NextID() (int64, error)
}

// Revoker 写入吊销名单，所有服务据此拒绝已吊销的访问令牌。
type Revoker interface {
	RevokeSessions(ctx context.Context, ttl time.Duration, sessionIDs ...int64) error
	RevokeUser(ctx context.Context, userID int64, at time.Time, ttl time.Duration) error
}
