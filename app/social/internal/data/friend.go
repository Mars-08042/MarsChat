package data

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Mars-08042/MarsChat/app/social/internal/biz"
)

// friendshipPO 对应 friendships 表。
type friendshipPO struct {
	UserID    int64 `gorm:"primaryKey;autoIncrement:false"`
	FriendID  int64 `gorm:"primaryKey;autoIncrement:false"`
	CreatedAt time.Time
}

func (friendshipPO) TableName() string { return "friendships" }

type friendRepo struct{ d *Data }

// NewFriendRepo 创建好友关系存储。
func NewFriendRepo(d *Data) biz.FriendRepo {
	return &friendRepo{d: d}
}

func (r *friendRepo) AreFriends(ctx context.Context, userID, friendID int64) (bool, error) {
	var n int64
	err := r.d.DB(ctx).Model(&friendshipPO{}).
		Where("user_id = ? AND friend_id = ?", userID, friendID).Limit(1).Count(&n).Error
	return n > 0, err
}

// AddPair 用 INSERT IGNORE 插入两个方向：两人互发申请并发同意时，后提交的一方不报错。
func (r *friendRepo) AddPair(ctx context.Context, a, b int64, now time.Time) (bool, error) {
	res := r.d.DB(ctx).Clauses(clause.Insert{Modifier: "IGNORE"}).Create(&[]friendshipPO{
		{UserID: a, FriendID: b, CreatedAt: now},
		{UserID: b, FriendID: a, CreatedAt: now},
	})
	return res.RowsAffected > 0, res.Error
}

func (r *friendRepo) DeletePair(ctx context.Context, a, b int64) (bool, error) {
	res := r.d.DB(ctx).Where("(user_id = ? AND friend_id = ?) OR (user_id = ? AND friend_id = ?)", a, b, b, a).
		Delete(&friendshipPO{})
	return res.RowsAffected > 0, res.Error
}

func (r *friendRepo) FilterFriends(ctx context.Context, userID int64, ids []int64) ([]int64, error) {
	var out []int64
	if len(ids) == 0 {
		return out, nil
	}
	err := r.d.DB(ctx).Model(&friendshipPO{}).
		Where("user_id = ? AND friend_id IN ?", userID, ids).Pluck("friend_id", &out).Error
	return out, err
}

func (r *friendRepo) List(ctx context.Context, userID, after int64, limit int) ([]*biz.Friend, error) {
	var pos []friendshipPO
	err := r.d.DB(ctx).Where("user_id = ? AND friend_id > ?", userID, after).
		Order("friend_id").Limit(limit).Find(&pos).Error
	if err != nil {
		return nil, err
	}
	out := make([]*biz.Friend, len(pos))
	for i, p := range pos {
		out[i] = &biz.Friend{UserID: p.UserID, FriendID: p.FriendID, CreatedAt: p.CreatedAt}
	}
	return out, nil
}

// friendRequestPO 对应 friend_requests 表。生成列 pending_flag 由数据库维护，这里不映射。
type friendRequestPO struct {
	ID         int64 `gorm:"primaryKey;autoIncrement:false"`
	FromUserID int64
	ToUserID   int64
	Message    string
	Status     int32
	CreatedAt  time.Time
	UpdatedAt  time.Time
	HandledAt  *time.Time
}

func (friendRequestPO) TableName() string { return "friend_requests" }

func (p *friendRequestPO) toBiz() *biz.FriendRequest {
	return &biz.FriendRequest{
		ID: p.ID, FromUserID: p.FromUserID, ToUserID: p.ToUserID, Message: p.Message, Status: p.Status,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt, HandledAt: p.HandledAt,
	}
}

func friendRequestsToBiz(pos []friendRequestPO) []*biz.FriendRequest {
	out := make([]*biz.FriendRequest, len(pos))
	for i := range pos {
		out[i] = pos[i].toBiz()
	}
	return out
}

type friendRequestRepo struct{ d *Data }

// NewFriendRequestRepo 创建好友申请存储。
func NewFriendRequestRepo(d *Data) biz.FriendRequestRepo {
	return &friendRequestRepo{d: d}
}

func (r *friendRequestRepo) Create(ctx context.Context, fr *biz.FriendRequest) error {
	err := r.d.DB(ctx).Create(&friendRequestPO{
		ID: fr.ID, FromUserID: fr.FromUserID, ToUserID: fr.ToUserID, Message: fr.Message, Status: fr.Status,
		CreatedAt: fr.CreatedAt, UpdatedAt: fr.UpdatedAt,
	}).Error
	// 主键是 snowflake 不会冲突，唯一键冲突只可能来自 uk_pending
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return biz.ErrDuplicate
	}
	return err
}

func (r *friendRequestRepo) find(ctx context.Context, query string, args ...any) (*biz.FriendRequest, error) {
	var po friendRequestPO
	err := r.d.DB(ctx).Where(query, args...).Take(&po).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, biz.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return po.toBiz(), nil
}

func (r *friendRequestRepo) FindByID(ctx context.Context, id int64) (*biz.FriendRequest, error) {
	return r.find(ctx, "id = ?", id)
}

// FindPending 走唯一索引 uk_pending。
func (r *friendRequestRepo) FindPending(ctx context.Context, from, to int64) (*biz.FriendRequest, error) {
	return r.find(ctx, "from_user_id = ? AND to_user_id = ? AND pending_flag = 1", from, to)
}

func (r *friendRequestRepo) Refresh(ctx context.Context, id int64, message string, now time.Time) error {
	return r.d.DB(ctx).Model(&friendRequestPO{}).Where("id = ? AND status = ?", id, biz.RequestPending).
		Updates(map[string]any{"message": message, "updated_at": now}).Error
}

func (r *friendRequestRepo) Handle(ctx context.Context, id, toUserID int64, status int32, now time.Time) (bool, error) {
	res := r.d.DB(ctx).Model(&friendRequestPO{}).
		Where("id = ? AND to_user_id = ? AND status = ?", id, toUserID, biz.RequestPending).
		Updates(map[string]any{"status": status, "handled_at": now, "updated_at": now})
	return res.RowsAffected > 0, res.Error
}

func (r *friendRequestRepo) ResolvePending(ctx context.Context, from, to int64, status int32, now time.Time) error {
	return r.d.DB(ctx).Model(&friendRequestPO{}).
		Where("from_user_id = ? AND to_user_id = ? AND pending_flag = 1", from, to).
		Updates(map[string]any{"status": status, "handled_at": now, "updated_at": now}).Error
}

// ListReceived 走 idx_to (to_user_id, status, id)。
func (r *friendRequestRepo) ListReceived(ctx context.Context, toUserID, before int64, limit int) ([]*biz.FriendRequest, error) {
	q := r.d.DB(ctx).Where("to_user_id = ? AND status = ?", toUserID, biz.RequestPending)
	if before > 0 {
		q = q.Where("id < ?", before)
	}
	var pos []friendRequestPO
	if err := q.Order("id DESC").Limit(limit).Find(&pos).Error; err != nil {
		return nil, err
	}
	return friendRequestsToBiz(pos), nil
}

// ListSent 走 idx_from (from_user_id, id)。
func (r *friendRequestRepo) ListSent(ctx context.Context, fromUserID, before int64, limit int) ([]*biz.FriendRequest, error) {
	q := r.d.DB(ctx).Where("from_user_id = ?", fromUserID)
	if before > 0 {
		q = q.Where("id < ?", before)
	}
	var pos []friendRequestPO
	if err := q.Order("id DESC").Limit(limit).Find(&pos).Error; err != nil {
		return nil, err
	}
	return friendRequestsToBiz(pos), nil
}

// blockPO 对应 blocks 表。
type blockPO struct {
	UserID    int64 `gorm:"primaryKey;autoIncrement:false"`
	BlockedID int64 `gorm:"primaryKey;autoIncrement:false"`
	CreatedAt time.Time
}

func (blockPO) TableName() string { return "blocks" }

type blockRepo struct{ d *Data }

// NewBlockRepo 创建拉黑存储。
func NewBlockRepo(d *Data) biz.BlockRepo {
	return &blockRepo{d: d}
}

func (r *blockRepo) Add(ctx context.Context, userID, blockedID int64, now time.Time) error {
	return r.d.DB(ctx).Clauses(clause.Insert{Modifier: "IGNORE"}).
		Create(&blockPO{UserID: userID, BlockedID: blockedID, CreatedAt: now}).Error
}

func (r *blockRepo) Remove(ctx context.Context, userID, blockedID int64) error {
	return r.d.DB(ctx).Where("user_id = ? AND blocked_id = ?", userID, blockedID).Delete(&blockPO{}).Error
}

// EitherBlocked 用行构造器 IN 一次查两个方向，MySQL 按主键做两次点查。
func (r *blockRepo) EitherBlocked(ctx context.Context, a, b int64) (bool, error) {
	var n int64
	err := r.d.DB(ctx).Model(&blockPO{}).
		Where("(user_id, blocked_id) IN ((?, ?), (?, ?))", a, b, b, a).Limit(1).Count(&n).Error
	return n > 0, err
}

func (r *blockRepo) List(ctx context.Context, userID, after int64, limit int) ([]*biz.Block, error) {
	var pos []blockPO
	err := r.d.DB(ctx).Where("user_id = ? AND blocked_id > ?", userID, after).
		Order("blocked_id").Limit(limit).Find(&pos).Error
	if err != nil {
		return nil, err
	}
	out := make([]*biz.Block, len(pos))
	for i, p := range pos {
		out[i] = &biz.Block{UserID: p.UserID, BlockedID: p.BlockedID, CreatedAt: p.CreatedAt}
	}
	return out, nil
}
