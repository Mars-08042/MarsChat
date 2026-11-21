package data

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Mars-08042/MarsChat/app/account/internal/biz"
)

// sessionPO 对应 sessions 表。生成列 active_device 由数据库维护，这里不映射。
type sessionPO struct {
	ID               int64 `gorm:"primaryKey;autoIncrement:false"`
	UserID           int64
	DeviceID         string
	Platform         int32
	DeviceName       string
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

func (sessionPO) TableName() string { return "sessions" }

func (p *sessionPO) toBiz() *biz.Session {
	return &biz.Session{
		ID: p.ID, UserID: p.UserID,
		Device:      biz.Device{ID: p.DeviceID, Platform: p.Platform, Name: p.DeviceName},
		RefreshHash: p.RefreshHash, PrevRefreshHash: p.PrevRefreshHash, RotatedAt: p.RotatedAt,
		RefreshExpiresAt: p.RefreshExpiresAt, LoginIP: p.LoginIP, LastIP: p.LastIP,
		LastActiveAt: p.LastActiveAt, RevokedAt: p.RevokedAt, RevokeReason: p.RevokeReason, CreatedAt: p.CreatedAt,
	}
}

type sessionRepo struct{ d *Data }

// NewSessionRepo 创建会话存储。
func NewSessionRepo(d *Data) biz.SessionRepo {
	return &sessionRepo{d: d}
}

func (r *sessionRepo) Create(ctx context.Context, s *biz.Session) error {
	return r.d.DB(ctx).Create(&sessionPO{
		ID: s.ID, UserID: s.UserID, DeviceID: s.Device.ID, Platform: s.Device.Platform, DeviceName: s.Device.Name,
		RefreshHash: s.RefreshHash, RefreshExpiresAt: s.RefreshExpiresAt,
		LoginIP: s.LoginIP, LastIP: s.LastIP, LastActiveAt: s.LastActiveAt, CreatedAt: s.CreatedAt,
	}).Error
}

func (r *sessionRepo) FindByID(ctx context.Context, id int64) (*biz.Session, error) {
	var po sessionPO
	err := r.d.DB(ctx).Where("id = ?", id).Take(&po).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, biz.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return po.toBiz(), nil
}

// RevokeActiveByDevice 先加锁读出会话 ID（事件里需要），再吊销。查询走唯一索引 uk_user_active_device。
func (r *sessionRepo) RevokeActiveByDevice(ctx context.Context, userID int64, deviceID string, reason int32, at time.Time) (int64, error) {
	var ids []int64
	err := r.d.DB(ctx).Model(&sessionPO{}).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("user_id = ? AND active_device = ?", userID, deviceID).Pluck("id", &ids).Error
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	if err := r.revokeIDs(ctx, ids, reason, at); err != nil {
		return 0, err
	}
	return ids[0], nil
}

func (r *sessionRepo) Revoke(ctx context.Context, id, userID int64, reason int32, at time.Time) (bool, error) {
	res := r.d.DB(ctx).Model(&sessionPO{}).
		Where("id = ? AND user_id = ? AND revoked_at IS NULL", id, userID).
		Updates(map[string]any{"revoked_at": at, "revoke_reason": reason})
	return res.RowsAffected > 0, res.Error
}

// RevokeAllByUser 走索引 idx_user_revoked。
func (r *sessionRepo) RevokeAllByUser(ctx context.Context, userID int64, reason int32, at time.Time) ([]int64, error) {
	var ids []int64
	err := r.d.DB(ctx).Model(&sessionPO{}).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("user_id = ? AND revoked_at IS NULL", userID).Pluck("id", &ids).Error
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	if err := r.revokeIDs(ctx, ids, reason, at); err != nil {
		return nil, err
	}
	return ids, nil
}

func (r *sessionRepo) revokeIDs(ctx context.Context, ids []int64, reason int32, at time.Time) error {
	return r.d.DB(ctx).Model(&sessionPO{}).
		Where("id IN ? AND revoked_at IS NULL", ids).
		Updates(map[string]any{"revoked_at": at, "revoke_reason": reason}).Error
}

// Rotate 用手写 SQL：MySQL 单表 UPDATE 按书写顺序从左到右赋值，prev_refresh_hash 必须先于
// refresh_hash 赋值才能拿到旧值，而 gorm 的 map 更新不保证列顺序。
func (r *sessionRepo) Rotate(ctx context.Context, id int64, oldHash, newHash []byte, expiresAt time.Time, ip string, at time.Time) (bool, error) {
	res := r.d.DB(ctx).Exec(`UPDATE sessions
SET prev_refresh_hash = refresh_hash, refresh_hash = ?, rotated_at = ?, refresh_expires_at = ?, last_ip = ?, last_active_at = ?
WHERE id = ? AND refresh_hash = ? AND revoked_at IS NULL`,
		newHash, at, expiresAt, ip, at, id, oldHash)
	return res.RowsAffected > 0, res.Error
}

func (r *sessionRepo) ListActive(ctx context.Context, userID int64) ([]*biz.Session, error) {
	var pos []sessionPO
	err := r.d.DB(ctx).Where("user_id = ? AND revoked_at IS NULL", userID).
		Order("last_active_at DESC").Find(&pos).Error
	if err != nil {
		return nil, err
	}
	out := make([]*biz.Session, len(pos))
	for i := range pos {
		out[i] = pos[i].toBiz()
	}
	return out, nil
}
