package data

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Mars-08042/MarsChat/app/social/internal/biz"
	"github.com/Mars-08042/MarsChat/app/social/internal/conf"
	"github.com/Mars-08042/MarsChat/pkg/bloom"
	"github.com/Mars-08042/MarsChat/pkg/cache"
)

// groupPO 对应 chat_groups 表。
type groupPO struct {
	ID          int64 `gorm:"primaryKey;autoIncrement:false"`
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

func (groupPO) TableName() string { return "chat_groups" }

func (p *groupPO) toBiz() *biz.Group {
	return &biz.Group{
		ID: p.ID, Name: p.Name, Avatar: p.Avatar, Notice: p.Notice, OwnerID: p.OwnerID, JoinMode: p.JoinMode,
		MuteAll: p.MuteAll, MemberCount: p.MemberCount, Status: p.Status, Version: p.Version,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt, DismissedAt: p.DismissedAt,
	}
}

// cachedGroup 是缓存中的群资料。字段变化时旧缓存按 JSON 规则解码，新增字段为零值，
// 最多在一个 TTL 内不完整；改变字段含义时要换键名。
type cachedGroup struct {
	ID          int64      `json:"id"`
	Name        string     `json:"name"`
	Avatar      string     `json:"avatar,omitempty"`
	Notice      string     `json:"notice,omitempty"`
	OwnerID     int64      `json:"owner_id"`
	JoinMode    int32      `json:"join_mode"`
	MuteAll     bool       `json:"mute_all,omitempty"`
	MemberCount int32      `json:"member_count"`
	Status      int32      `json:"status"`
	Version     int32      `json:"version"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	DismissedAt *time.Time `json:"dismissed_at,omitempty"`
}

func newCachedGroup(p *groupPO) cachedGroup {
	return cachedGroup{
		ID: p.ID, Name: p.Name, Avatar: p.Avatar, Notice: p.Notice, OwnerID: p.OwnerID, JoinMode: p.JoinMode,
		MuteAll: p.MuteAll, MemberCount: p.MemberCount, Status: p.Status, Version: p.Version,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt, DismissedAt: p.DismissedAt,
	}
}

func (c cachedGroup) toBiz() *biz.Group {
	return &biz.Group{
		ID: c.ID, Name: c.Name, Avatar: c.Avatar, Notice: c.Notice, OwnerID: c.OwnerID, JoinMode: c.JoinMode,
		MuteAll: c.MuteAll, MemberCount: c.MemberCount, Status: c.Status, Version: c.Version,
		CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt, DismissedAt: c.DismissedAt,
	}
}

func groupCacheKey(id int64) string {
	return "social:group:" + strconv.FormatInt(id, 10)
}

type groupRepo struct {
	d      *Data
	cache  *cache.Cache[cachedGroup]
	bloom  *bloom.Filter
	logger *slog.Logger
}

// NewGroupRepo 创建群资料存储，并在群 ID 布隆过滤器未就绪时后台重建。
func NewGroupRepo(d *Data, c *conf.Cache, logger *slog.Logger) biz.GroupRepo {
	r := &groupRepo{
		d: d,
		cache: cache.New[cachedGroup](d.rdb, cache.Options{
			TTL:     c.GetGroupTtl().AsDuration(),
			NullTTL: c.GetNullTtl().AsDuration(),
		}, logger),
		logger: logger,
	}
	r.bloom = bloom.New(d.rdb, bloom.Options{
		Key:       "social:group_bf",
		Capacity:  1_000_000,
		ErrorRate: 0.001,
		Scan:      r.scanIDs,
	}, logger)
	r.bloom.EnsureReady(context.Background())
	return r
}

func (r *groupRepo) Create(ctx context.Context, g *biz.Group) error {
	return r.d.DB(ctx).Create(&groupPO{
		ID: g.ID, Name: g.Name, Avatar: g.Avatar, Notice: g.Notice, OwnerID: g.OwnerID, JoinMode: g.JoinMode,
		MuteAll: g.MuteAll, MemberCount: g.MemberCount, Status: g.Status, Version: g.Version,
		CreatedAt: g.CreatedAt, UpdatedAt: g.UpdatedAt,
	}).Error
}

func (r *groupRepo) findPO(ctx context.Context, db *gorm.DB, id int64) (*groupPO, error) {
	var po groupPO
	err := db.Where("id = ?", id).Take(&po).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, biz.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &po, nil
}

func (r *groupRepo) FindByID(ctx context.Context, id int64) (*biz.Group, error) {
	po, err := r.findPO(ctx, r.d.DB(ctx), id)
	if err != nil {
		return nil, err
	}
	return po.toBiz(), nil
}

func (r *groupRepo) Lock(ctx context.Context, id int64) (*biz.Group, error) {
	po, err := r.findPO(ctx, r.d.DB(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}), id)
	if err != nil {
		return nil, err
	}
	return po.toBiz(), nil
}

func (r *groupRepo) UpdateProfile(ctx context.Context, id int64, version int32, upd biz.GroupUpdate, now time.Time) (bool, error) {
	m := map[string]any{"updated_at": now, "version": gorm.Expr("version + 1")}
	if upd.Name != nil {
		m["name"] = *upd.Name
	}
	if upd.Avatar != nil {
		m["avatar"] = *upd.Avatar
	}
	if upd.Notice != nil {
		m["notice"] = *upd.Notice
	}
	if upd.JoinMode != nil {
		m["join_mode"] = *upd.JoinMode
	}
	if upd.MuteAll != nil {
		m["mute_all"] = *upd.MuteAll
	}
	res := r.d.DB(ctx).Model(&groupPO{}).
		Where("id = ? AND version = ? AND status = ?", id, version, biz.GroupNormal).Updates(m)
	return res.RowsAffected > 0, res.Error
}

// AddMemberCount 不修改 updated_at：它表示资料修改时间，成员进出不算。
func (r *groupRepo) AddMemberCount(ctx context.Context, id int64, delta int) error {
	return r.d.DB(ctx).Model(&groupPO{}).Where("id = ?", id).
		UpdateColumn("member_count", gorm.Expr("member_count + ?", delta)).Error
}

func (r *groupRepo) SetOwner(ctx context.Context, id, ownerID int64, now time.Time) error {
	return r.d.DB(ctx).Model(&groupPO{}).Where("id = ?", id).
		Updates(map[string]any{"owner_id": ownerID, "version": gorm.Expr("version + 1"), "updated_at": now}).Error
}

func (r *groupRepo) Dismiss(ctx context.Context, id int64, now time.Time) error {
	return r.d.DB(ctx).Model(&groupPO{}).Where("id = ?", id).Updates(map[string]any{
		"status": biz.GroupDismissed, "member_count": 0, "version": gorm.Expr("version + 1"),
		"updated_at": now, "dismissed_at": now,
	}).Error
}

func (r *groupRepo) GetCached(ctx context.Context, id int64) (*biz.Group, error) {
	v, err := r.cache.Get(ctx, groupCacheKey(id), func(ctx context.Context) (cachedGroup, error) {
		// 只在缓存未命中时查布隆过滤器，判定不存在时同样写入空值标记
		if !r.bloom.MightExist(ctx, id) {
			return cachedGroup{}, cache.ErrNotFound
		}
		po, err := r.findPO(ctx, r.d.db.WithContext(ctx), id)
		if errors.Is(err, biz.ErrNotFound) {
			return cachedGroup{}, cache.ErrNotFound
		}
		if err != nil {
			return cachedGroup{}, err
		}
		return newCachedGroup(po), nil
	})
	if errors.Is(err, cache.ErrNotFound) {
		return nil, biz.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return v.toBiz(), nil
}

// BatchGetCached 的 ID 来自成员表或会话，基本都存在，所以不查布隆过滤器。
func (r *groupRepo) BatchGetCached(ctx context.Context, ids []int64) ([]*biz.Group, error) {
	keys := make([]string, len(ids))
	byKey := make(map[string]int64, len(ids))
	for i, id := range ids {
		keys[i] = groupCacheKey(id)
		byKey[keys[i]] = id
	}
	got, err := r.cache.GetMany(ctx, keys, func(ctx context.Context, missing []string) (map[string]cachedGroup, error) {
		missingIDs := make([]int64, len(missing))
		for i, k := range missing {
			missingIDs[i] = byKey[k]
		}
		var pos []groupPO
		if err := r.d.db.WithContext(ctx).Where("id IN ?", missingIDs).Find(&pos).Error; err != nil {
			return nil, err
		}
		out := make(map[string]cachedGroup, len(pos))
		for i := range pos {
			out[groupCacheKey(pos[i].ID)] = newCachedGroup(&pos[i])
		}
		return out, nil
	})
	if err != nil {
		return nil, err
	}
	groups := make([]*biz.Group, 0, len(got))
	for _, k := range keys {
		if v, ok := got[k]; ok {
			groups = append(groups, v.toBiz())
		}
	}
	return groups, nil
}

func (r *groupRepo) EvictCache(ctx context.Context, id int64) {
	if err := r.cache.Delete(context.WithoutCancel(ctx), groupCacheKey(id)); err != nil {
		r.logger.Warn("evict group cache failed", slog.Int64("group_id", id), slog.Any("error", err))
	}
}

func (r *groupRepo) OnCreated(ctx context.Context, id int64) {
	r.bloom.Add(ctx, id)
}

// scanIDs 供布隆过滤器重建时分页扫描全部群 ID（含已解散的群，它们仍可查询）。
func (r *groupRepo) scanIDs(ctx context.Context, after int64, limit int) ([]int64, error) {
	var ids []int64
	err := r.d.db.WithContext(ctx).Model(&groupPO{}).
		Where("id > ?", after).Order("id").Limit(limit).Pluck("id", &ids).Error
	return ids, err
}

// memberPO 对应 group_members 表。
type memberPO struct {
	GroupID    int64 `gorm:"primaryKey;autoIncrement:false"`
	UserID     int64 `gorm:"primaryKey;autoIncrement:false"`
	Role       int32
	MutedUntil *time.Time
	InviterID  int64
	JoinedAt   time.Time
}

func (memberPO) TableName() string { return "group_members" }

func (p *memberPO) toBiz() *biz.Member {
	return &biz.Member{
		GroupID: p.GroupID, UserID: p.UserID, Role: p.Role, MutedUntil: p.MutedUntil,
		InviterID: p.InviterID, JoinedAt: p.JoinedAt,
	}
}

func membersToBiz(pos []memberPO) []*biz.Member {
	out := make([]*biz.Member, len(pos))
	for i := range pos {
		out[i] = pos[i].toBiz()
	}
	return out
}

// cachedMember 是成员列表缓存中的一项，只保留权限校验和投递需要的字段。
type cachedMember struct {
	UserID     int64      `json:"user_id"`
	Role       int32      `json:"role"`
	MutedUntil *time.Time `json:"muted_until,omitempty"`
}

func membersCacheKey(groupID int64) string {
	return "social:group:members:" + strconv.FormatInt(groupID, 10)
}

type memberRepo struct {
	d      *Data
	cache  *cache.Cache[[]cachedMember]
	logger *slog.Logger
}

// NewMemberRepo 创建群成员存储。成员列表每条群消息都要读，失效后多个实例会同时回源，所以开启跨实例重建锁。
func NewMemberRepo(d *Data, c *conf.Cache, logger *slog.Logger) biz.MemberRepo {
	return &memberRepo{
		d: d,
		cache: cache.New[[]cachedMember](d.rdb, cache.Options{
			TTL:         c.GetMembersTtl().AsDuration(),
			NullTTL:     c.GetNullTtl().AsDuration(),
			RebuildLock: true,
		}, logger),
		logger: logger,
	}
}

func (r *memberRepo) Find(ctx context.Context, groupID, userID int64) (*biz.Member, error) {
	var po memberPO
	err := r.d.DB(ctx).Where("group_id = ? AND user_id = ?", groupID, userID).Take(&po).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, biz.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return po.toBiz(), nil
}

func (r *memberRepo) FilterMembers(ctx context.Context, groupID int64, userIDs []int64) ([]int64, error) {
	var out []int64
	if len(userIDs) == 0 {
		return out, nil
	}
	err := r.d.DB(ctx).Model(&memberPO{}).
		Where("group_id = ? AND user_id IN ?", groupID, userIDs).Pluck("user_id", &out).Error
	return out, err
}

func (r *memberRepo) Add(ctx context.Context, members []*biz.Member) error {
	if len(members) == 0 {
		return nil
	}
	pos := make([]memberPO, len(members))
	for i, m := range members {
		pos[i] = memberPO{
			GroupID: m.GroupID, UserID: m.UserID, Role: m.Role, MutedUntil: m.MutedUntil,
			InviterID: m.InviterID, JoinedAt: m.JoinedAt,
		}
	}
	return r.d.DB(ctx).Create(&pos).Error
}

func (r *memberRepo) Remove(ctx context.Context, groupID, userID int64) (bool, error) {
	res := r.d.DB(ctx).Where("group_id = ? AND user_id = ?", groupID, userID).Delete(&memberPO{})
	return res.RowsAffected > 0, res.Error
}

func (r *memberRepo) SetRole(ctx context.Context, groupID, userID int64, role int32) error {
	return r.d.DB(ctx).Model(&memberPO{}).Where("group_id = ? AND user_id = ?", groupID, userID).
		Update("role", role).Error
}

func (r *memberRepo) SetMutedUntil(ctx context.Context, groupID, userID int64, until *time.Time) error {
	return r.d.DB(ctx).Model(&memberPO{}).Where("group_id = ? AND user_id = ?", groupID, userID).
		Update("muted_until", until).Error
}

func (r *memberRepo) RemoveAll(ctx context.Context, groupID int64) ([]int64, error) {
	var ids []int64
	if err := r.d.DB(ctx).Model(&memberPO{}).Where("group_id = ?", groupID).Order("user_id").
		Pluck("user_id", &ids).Error; err != nil {
		return nil, err
	}
	if err := r.d.DB(ctx).Where("group_id = ?", groupID).Delete(&memberPO{}).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

func (r *memberRepo) List(ctx context.Context, groupID, after int64, limit int) ([]*biz.Member, error) {
	var pos []memberPO
	err := r.d.DB(ctx).Where("group_id = ? AND user_id > ?", groupID, after).
		Order("user_id").Limit(limit).Find(&pos).Error
	if err != nil {
		return nil, err
	}
	return membersToBiz(pos), nil
}

// ListByUser 走 idx_user (user_id, group_id)。
func (r *memberRepo) ListByUser(ctx context.Context, userID, after int64, limit int) ([]*biz.Member, error) {
	var pos []memberPO
	err := r.d.DB(ctx).Where("user_id = ? AND group_id > ?", userID, after).
		Order("group_id").Limit(limit).Find(&pos).Error
	if err != nil {
		return nil, err
	}
	return membersToBiz(pos), nil
}

func (r *memberRepo) GetAllCached(ctx context.Context, groupID int64) ([]*biz.Member, error) {
	list, err := r.cache.Get(ctx, membersCacheKey(groupID), func(ctx context.Context) ([]cachedMember, error) {
		var pos []memberPO
		if err := r.d.db.WithContext(ctx).Select("user_id", "role", "muted_until").
			Where("group_id = ?", groupID).Order("user_id").Find(&pos).Error; err != nil {
			return nil, err
		}
		out := make([]cachedMember, len(pos))
		for i, p := range pos {
			out[i] = cachedMember{UserID: p.UserID, Role: p.Role, MutedUntil: p.MutedUntil}
		}
		return out, nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]*biz.Member, len(list))
	for i, m := range list {
		out[i] = &biz.Member{GroupID: groupID, UserID: m.UserID, Role: m.Role, MutedUntil: m.MutedUntil}
	}
	return out, nil
}

func (r *memberRepo) EvictCache(ctx context.Context, groupID int64) {
	if err := r.cache.Delete(context.WithoutCancel(ctx), membersCacheKey(groupID)); err != nil {
		r.logger.Warn("evict group members cache failed", slog.Int64("group_id", groupID), slog.Any("error", err))
	}
}

type groupCacheInvalidator struct{ d *Data }

// NewGroupCacheInvalidator 创建群缓存失效器，供事件消费者使用。
func NewGroupCacheInvalidator(d *Data) biz.GroupCacheInvalidator {
	return &groupCacheInvalidator{d: d}
}

func (i *groupCacheInvalidator) InvalidateGroup(ctx context.Context, groupID int64) error {
	return i.d.rdb.Del(ctx, groupCacheKey(groupID), membersCacheKey(groupID)).Err()
}

// joinRequestPO 对应 group_join_requests 表。生成列 pending_flag 由数据库维护，这里不映射。
type joinRequestPO struct {
	ID        int64 `gorm:"primaryKey;autoIncrement:false"`
	GroupID   int64
	UserID    int64
	Message   string
	Status    int32
	HandledBy int64
	CreatedAt time.Time
	UpdatedAt time.Time
	HandledAt *time.Time
}

func (joinRequestPO) TableName() string { return "group_join_requests" }

func (p *joinRequestPO) toBiz() *biz.JoinRequest {
	return &biz.JoinRequest{
		ID: p.ID, GroupID: p.GroupID, UserID: p.UserID, Message: p.Message, Status: p.Status,
		HandledBy: p.HandledBy, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt, HandledAt: p.HandledAt,
	}
}

type joinRequestRepo struct{ d *Data }

// NewJoinRequestRepo 创建入群申请存储。
func NewJoinRequestRepo(d *Data) biz.JoinRequestRepo {
	return &joinRequestRepo{d: d}
}

func (r *joinRequestRepo) Create(ctx context.Context, jr *biz.JoinRequest) error {
	err := r.d.DB(ctx).Create(&joinRequestPO{
		ID: jr.ID, GroupID: jr.GroupID, UserID: jr.UserID, Message: jr.Message, Status: jr.Status,
		CreatedAt: jr.CreatedAt, UpdatedAt: jr.UpdatedAt,
	}).Error
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return biz.ErrDuplicate
	}
	return err
}

func (r *joinRequestRepo) find(ctx context.Context, query string, args ...any) (*biz.JoinRequest, error) {
	var po joinRequestPO
	err := r.d.DB(ctx).Where(query, args...).Take(&po).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, biz.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return po.toBiz(), nil
}

func (r *joinRequestRepo) FindByID(ctx context.Context, id int64) (*biz.JoinRequest, error) {
	return r.find(ctx, "id = ?", id)
}

func (r *joinRequestRepo) FindPending(ctx context.Context, groupID, userID int64) (*biz.JoinRequest, error) {
	return r.find(ctx, "group_id = ? AND user_id = ? AND pending_flag = 1", groupID, userID)
}

func (r *joinRequestRepo) Refresh(ctx context.Context, id int64, message string, now time.Time) error {
	return r.d.DB(ctx).Model(&joinRequestPO{}).Where("id = ? AND status = ?", id, biz.RequestPending).
		Updates(map[string]any{"message": message, "updated_at": now}).Error
}

func (r *joinRequestRepo) Handle(ctx context.Context, id int64, status int32, handledBy int64, now time.Time) (bool, error) {
	res := r.d.DB(ctx).Model(&joinRequestPO{}).Where("id = ? AND status = ?", id, biz.RequestPending).
		Updates(map[string]any{"status": status, "handled_by": handledBy, "handled_at": now, "updated_at": now})
	return res.RowsAffected > 0, res.Error
}

func (r *joinRequestRepo) ExpireForUsers(ctx context.Context, groupID int64, userIDs []int64, now time.Time) error {
	if len(userIDs) == 0 {
		return nil
	}
	return r.d.DB(ctx).Model(&joinRequestPO{}).
		Where("group_id = ? AND user_id IN ? AND pending_flag = 1", groupID, userIDs).
		Updates(map[string]any{"status": biz.RequestExpired, "handled_at": now, "updated_at": now}).Error
}

// ExpireAll 走 idx_group (group_id, status, id)。
func (r *joinRequestRepo) ExpireAll(ctx context.Context, groupID int64, now time.Time) error {
	return r.d.DB(ctx).Model(&joinRequestPO{}).
		Where("group_id = ? AND status = ?", groupID, biz.RequestPending).
		Updates(map[string]any{"status": biz.RequestExpired, "handled_at": now, "updated_at": now}).Error
}

func (r *joinRequestRepo) ListPending(ctx context.Context, groupID, before int64, limit int) ([]*biz.JoinRequest, error) {
	q := r.d.DB(ctx).Where("group_id = ? AND status = ?", groupID, biz.RequestPending)
	if before > 0 {
		q = q.Where("id < ?", before)
	}
	var pos []joinRequestPO
	if err := q.Order("id DESC").Limit(limit).Find(&pos).Error; err != nil {
		return nil, err
	}
	out := make([]*biz.JoinRequest, len(pos))
	for i := range pos {
		out[i] = pos[i].toBiz()
	}
	return out, nil
}
