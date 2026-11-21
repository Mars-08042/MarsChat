package biz

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// 生日不能早于这一天，挡掉明显错误的输入
var minBirthday = time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC)

// UserUsecase 负责用户资料。
type UserUsecase struct {
	users  UserRepo
	logger *slog.Logger
	now    func() time.Time
}

// NewUserUsecase 创建资料用例。
func NewUserUsecase(users UserRepo, logger *slog.Logger) *UserUsecase {
	return &UserUsecase{users: users, logger: logger, now: nowUTC}
}

// ParseBirthday 解析 YYYY-MM-DD；空字符串表示清除生日，返回 nil。
func ParseBirthday(s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return nil, ErrInvalidBirthday()
	}
	return &t, nil
}

// GetMe 返回自己的完整资料。
func (uc *UserUsecase) GetMe(ctx context.Context, id int64) (*User, error) {
	return uc.get(ctx, id)
}

// GetUser 返回他人的资料，调用方只能对外暴露公开字段。
func (uc *UserUsecase) GetUser(ctx context.Context, id int64) (*User, error) {
	return uc.get(ctx, id)
}

func (uc *UserUsecase) get(ctx context.Context, id int64) (*User, error) {
	u, err := uc.users.GetCached(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrUserNotFound()
	}
	return u, err
}

// BatchGet 批量获取资料，不存在的 ID 直接跳过。
func (uc *UserUsecase) BatchGet(ctx context.Context, ids []int64) ([]*User, error) {
	return uc.users.BatchGetCached(ctx, ids)
}

// UpdateMe 修改自己的资料，返回修改后的资料。
func (uc *UserUsecase) UpdateMe(ctx context.Context, id int64, upd ProfileUpdate) (*User, error) {
	if upd.SetBirthday && upd.Birthday != nil {
		// 按北京时间判断“今天”，当天出生也允许
		today := uc.now().In(dayZone)
		limit := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
		if upd.Birthday.After(limit) || upd.Birthday.Before(minBirthday) {
			return nil, ErrInvalidBirthday()
		}
	}
	if !upd.empty() {
		if err := uc.users.UpdateProfile(ctx, id, upd, uc.now()); err != nil {
			return nil, err
		}
		uc.users.EvictCache(ctx, id)
	}
	// 读库而不是读缓存：缓存刚被删除，回源也是读库，直接读还能避开删除失败时的旧值
	u, err := uc.users.FindByID(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrUserNotFound()
	}
	return u, err
}

func (u ProfileUpdate) empty() bool {
	return u.Nickname == nil && u.Avatar == nil && u.Gender == nil && u.Signature == nil && !u.SetBirthday
}
