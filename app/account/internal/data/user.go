package data

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Mars-08042/MarsChat/app/account/internal/biz"
	"github.com/Mars-08042/MarsChat/app/account/internal/conf"
	"github.com/Mars-08042/MarsChat/pkg/cache"
)

// userPO 对应 users 表。
type userPO struct {
	ID           int64 `gorm:"primaryKey;autoIncrement:false"`
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

func (userPO) TableName() string { return "users" }

func (p *userPO) toBiz() *biz.User {
	return &biz.User{
		ID: p.ID, Phone: p.Phone, PasswordHash: p.PasswordHash, Nickname: p.Nickname, Avatar: p.Avatar,
		Gender: p.Gender, Birthday: p.Birthday, Signature: p.Signature, Role: p.Role, Status: p.Status,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

// cachedUser 是缓存中的资料，不含密码哈希。字段变化时旧缓存按 JSON 规则解码，
// 新增字段为零值，最多在一个 TTL 内不完整；改变字段含义时要换键名。
type cachedUser struct {
	ID        int64      `json:"id"`
	Phone     string     `json:"phone"`
	Nickname  string     `json:"nickname"`
	Avatar    string     `json:"avatar,omitempty"`
	Gender    int32      `json:"gender,omitempty"`
	Birthday  *time.Time `json:"birthday,omitempty"`
	Signature string     `json:"signature,omitempty"`
	Role      int32      `json:"role"`
	Status    int32      `json:"status"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

func newCachedUser(p *userPO) cachedUser {
	return cachedUser{
		ID: p.ID, Phone: p.Phone, Nickname: p.Nickname, Avatar: p.Avatar, Gender: p.Gender,
		Birthday: p.Birthday, Signature: p.Signature, Role: p.Role, Status: p.Status,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

func (c cachedUser) toBiz() *biz.User {
	return &biz.User{
		ID: c.ID, Phone: c.Phone, Nickname: c.Nickname, Avatar: c.Avatar, Gender: c.Gender,
		Birthday: c.Birthday, Signature: c.Signature, Role: c.Role, Status: c.Status,
		CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
}

func userCacheKey(id int64) string {
	return "account:user:" + strconv.FormatInt(id, 10)
}

type userRepo struct {
	d      *Data
	cache  *cache.Cache[cachedUser]
	bloom  *userBloom
	logger *slog.Logger
}

// NewUserRepo 创建用户存储，并在布隆过滤器未就绪时后台重建。
func NewUserRepo(d *Data, c *conf.Cache, logger *slog.Logger) biz.UserRepo {
	r := &userRepo{
		d: d,
		cache: cache.New[cachedUser](d.rdb, cache.Options{
			TTL:     c.GetUserTtl().AsDuration(),
			NullTTL: c.GetNullTtl().AsDuration(),
		}, logger),
		bloom:  newUserBloom(d, logger),
		logger: logger,
	}
	r.bloom.ensureReady(context.Background())
	return r
}

func (r *userRepo) Create(ctx context.Context, u *biz.User) error {
	po := &userPO{
		ID: u.ID, Phone: u.Phone, PasswordHash: u.PasswordHash, Nickname: u.Nickname, Avatar: u.Avatar,
		Gender: u.Gender, Birthday: u.Birthday, Signature: u.Signature, Role: u.Role, Status: u.Status,
		CreatedAt: u.CreatedAt, UpdatedAt: u.UpdatedAt,
	}
	err := r.d.DB(ctx).Create(po).Error
	// 主键是 snowflake 不会冲突，唯一键冲突只可能来自 uk_phone
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return biz.ErrPhoneTaken
	}
	return err
}

func (r *userRepo) findPO(ctx context.Context, query string, arg any) (*userPO, error) {
	var po userPO
	err := r.d.DB(ctx).Where(query, arg).Take(&po).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, biz.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &po, nil
}

func (r *userRepo) FindByID(ctx context.Context, id int64) (*biz.User, error) {
	po, err := r.findPO(ctx, "id = ?", id)
	if err != nil {
		return nil, err
	}
	return po.toBiz(), nil
}

func (r *userRepo) FindByPhone(ctx context.Context, phone string) (*biz.User, error) {
	po, err := r.findPO(ctx, "phone = ?", phone)
	if err != nil {
		return nil, err
	}
	return po.toBiz(), nil
}

func (r *userRepo) ExistsByPhone(ctx context.Context, phone string) (bool, error) {
	var ids []int64
	err := r.d.DB(ctx).Model(&userPO{}).Where("phone = ?", phone).Limit(1).Pluck("id", &ids).Error
	return len(ids) > 0, err
}

func (r *userRepo) UpdateProfile(ctx context.Context, id int64, upd biz.ProfileUpdate, now time.Time) error {
	m := map[string]any{"updated_at": now}
	if upd.Nickname != nil {
		m["nickname"] = *upd.Nickname
	}
	if upd.Avatar != nil {
		m["avatar"] = *upd.Avatar
	}
	if upd.Gender != nil {
		m["gender"] = *upd.Gender
	}
	if upd.Signature != nil {
		m["signature"] = *upd.Signature
	}
	if upd.SetBirthday {
		m["birthday"] = upd.Birthday
	}
	return r.d.DB(ctx).Model(&userPO{}).Where("id = ?", id).Updates(m).Error
}

func (r *userRepo) SetStatus(ctx context.Context, id int64, status int32, now time.Time) (bool, error) {
	res := r.d.DB(ctx).Model(&userPO{}).
		Where("id = ? AND status <> ?", id, status).
		Updates(map[string]any{"status": status, "updated_at": now})
	return res.RowsAffected > 0, res.Error
}

func (r *userRepo) LockStatus(ctx context.Context, id int64) (int32, error) {
	var po userPO
	err := r.d.DB(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("status").Where("id = ?", id).Take(&po).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, biz.ErrNotFound
	}
	return po.Status, err
}

func (r *userRepo) GetCached(ctx context.Context, id int64) (*biz.User, error) {
	v, err := r.cache.Get(ctx, userCacheKey(id), func(ctx context.Context) (cachedUser, error) {
		// 只在缓存未命中时查布隆过滤器：命中时少一次 Redis 往返；
		// 判定不存在时同样写入空值标记，短时间内重复的非法 ID 连过滤器也不用查
		if !r.bloom.mightExist(ctx, id) {
			return cachedUser{}, cache.ErrNotFound
		}
		po, err := r.findPO(ctx, "id = ?", id)
		if errors.Is(err, biz.ErrNotFound) {
			return cachedUser{}, cache.ErrNotFound
		}
		if err != nil {
			return cachedUser{}, err
		}
		return newCachedUser(po), nil
	})
	if errors.Is(err, cache.ErrNotFound) {
		return nil, biz.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return v.toBiz(), nil
}

// BatchGetCached 按输入顺序返回存在的用户。批量接口的 ID 来自好友、群成员等内部数据，
// 基本都存在，所以不查布隆过滤器。
func (r *userRepo) BatchGetCached(ctx context.Context, ids []int64) ([]*biz.User, error) {
	keys := make([]string, len(ids))
	byKey := make(map[string]int64, len(ids))
	for i, id := range ids {
		keys[i] = userCacheKey(id)
		byKey[keys[i]] = id
	}
	got, err := r.cache.GetMany(ctx, keys, func(ctx context.Context, missing []string) (map[string]cachedUser, error) {
		missingIDs := make([]int64, len(missing))
		for i, k := range missing {
			missingIDs[i] = byKey[k]
		}
		var pos []userPO
		if err := r.d.DB(ctx).Where("id IN ?", missingIDs).Find(&pos).Error; err != nil {
			return nil, err
		}
		out := make(map[string]cachedUser, len(pos))
		for i := range pos {
			out[userCacheKey(pos[i].ID)] = newCachedUser(&pos[i])
		}
		return out, nil
	})
	if err != nil {
		return nil, err
	}
	users := make([]*biz.User, 0, len(got))
	for _, k := range keys {
		if v, ok := got[k]; ok {
			users = append(users, v.toBiz())
		}
	}
	return users, nil
}

func (r *userRepo) EvictCache(ctx context.Context, id int64) {
	if err := r.cache.Delete(context.WithoutCancel(ctx), userCacheKey(id)); err != nil {
		r.logger.Warn("evict user cache failed", slog.Int64("user_id", id), slog.Any("error", err))
	}
}

func (r *userRepo) OnRegistered(ctx context.Context, id int64) {
	r.bloom.add(context.WithoutCancel(ctx), id)
}
