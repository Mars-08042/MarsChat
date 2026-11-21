package data

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Mars-08042/MarsChat/app/message/internal/biz"
)

// conversationPO 对应 conversations 表。单聊以外 user_low、user_high 为 NULL。
type conversationPO struct {
	ID        int64 `gorm:"primaryKey;autoIncrement:false"`
	Type      int32
	UserLow   *int64
	UserHigh  *int64
	MaxSeq    int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (conversationPO) TableName() string { return "conversations" }

func (p *conversationPO) toBiz() *biz.Conversation {
	c := &biz.Conversation{ID: p.ID, Type: p.Type, MaxSeq: p.MaxSeq, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt}
	if p.UserLow != nil && p.UserHigh != nil {
		c.UserLow, c.UserHigh = *p.UserLow, *p.UserHigh
	}
	return c
}

type conversationRepo struct{ d *Data }

// NewConversationRepo 创建会话存储。
func NewConversationRepo(d *Data) biz.ConversationRepo {
	return &conversationRepo{d: d}
}

func (r *conversationRepo) first(db *gorm.DB) (*biz.Conversation, error) {
	var po conversationPO
	err := db.Take(&po).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, biz.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return po.toBiz(), nil
}

func (r *conversationRepo) Get(ctx context.Context, id int64) (*biz.Conversation, error) {
	return r.first(r.d.DB(ctx).Where("id = ?", id))
}

func (r *conversationRepo) FindPair(ctx context.Context, low, high int64) (*biz.Conversation, error) {
	return r.first(r.d.DB(ctx).Where("user_low = ? AND user_high = ?", low, high))
}

func (r *conversationRepo) BatchGet(ctx context.Context, ids []int64) (map[int64]*biz.Conversation, error) {
	out := make(map[int64]*biz.Conversation, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	var pos []conversationPO
	if err := r.d.DB(ctx).Where("id IN ?", ids).Find(&pos).Error; err != nil {
		return nil, err
	}
	for i := range pos {
		out[pos[i].ID] = pos[i].toBiz()
	}
	return out, nil
}

// CreateIfAbsent 生成 INSERT ... ON DUPLICATE KEY UPDATE id = id：重复时加排他锁且不修改任何列。
// 不用 INSERT IGNORE：它在重复时加共享锁，两个事务随后都要把同一行升级为排他锁，会互相死锁。
func (r *conversationRepo) CreateIfAbsent(ctx context.Context, c *biz.Conversation) error {
	po := &conversationPO{ID: c.ID, Type: c.Type, MaxSeq: 0, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt}
	if c.Type == biz.TypeSingle {
		po.UserLow, po.UserHigh = &c.UserLow, &c.UserHigh
	}
	return r.d.DB(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(po).Error
}

func (r *conversationRepo) Lock(ctx context.Context, id int64) (*biz.Conversation, error) {
	return r.first(r.d.DB(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Where("id = ?", id))
}

func (r *conversationRepo) LockPair(ctx context.Context, low, high int64) (*biz.Conversation, error) {
	return r.first(r.d.DB(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("user_low = ? AND user_high = ?", low, high))
}

func (r *conversationRepo) SetMaxSeq(ctx context.Context, id, seq int64, now time.Time) error {
	return r.d.DB(ctx).Model(&conversationPO{}).Where("id = ?", id).
		Updates(map[string]any{"max_seq": seq, "updated_at": now}).Error
}

// memberPO 对应 conversation_members 表。
type memberPO struct {
	ConversationID int64 `gorm:"primaryKey;autoIncrement:false"`
	UserID         int64 `gorm:"primaryKey;autoIncrement:false"`
	ReadSeq        int64
	JoinedAt       time.Time
	UpdatedAt      time.Time
}

func (memberPO) TableName() string { return "conversation_members" }

func (p *memberPO) toBiz() *biz.Member {
	return &biz.Member{ConversationID: p.ConversationID, UserID: p.UserID, ReadSeq: p.ReadSeq, JoinedAt: p.JoinedAt, UpdatedAt: p.UpdatedAt}
}

type memberRepo struct{ d *Data }

// NewMemberRepo 创建会话成员存储。
func NewMemberRepo(d *Data) biz.MemberRepo {
	return &memberRepo{d: d}
}

func (r *memberRepo) Get(ctx context.Context, conversationID, userID int64) (*biz.Member, error) {
	var po memberPO
	err := r.d.DB(ctx).Where("conversation_id = ? AND user_id = ?", conversationID, userID).Take(&po).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, biz.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return po.toBiz(), nil
}

func (r *memberRepo) AddIgnore(ctx context.Context, members []*biz.Member) error {
	if len(members) == 0 {
		return nil
	}
	pos := make([]memberPO, len(members))
	for i, m := range members {
		pos[i] = memberPO{ConversationID: m.ConversationID, UserID: m.UserID, ReadSeq: m.ReadSeq, JoinedAt: m.JoinedAt, UpdatedAt: m.UpdatedAt}
	}
	return r.d.DB(ctx).Clauses(clause.Insert{Modifier: "IGNORE"}).Create(&pos).Error
}

// AddAtLatest 先读会话当前的 max_seq，再批量插入。读和插入之间如果又有新消息，
// 这几条会算作新成员的未读，宁可多算也不漏掉入群之后的消息。
func (r *memberRepo) AddAtLatest(ctx context.Context, conversationID int64, userIDs []int64, now time.Time) error {
	if len(userIDs) == 0 {
		return nil
	}
	var maxSeq int64
	err := r.d.DB(ctx).Model(&conversationPO{}).Where("id = ?", conversationID).Pluck("max_seq", &maxSeq).Error
	if err != nil {
		return err
	}
	ms := make([]*biz.Member, len(userIDs))
	for i, uid := range userIDs {
		ms[i] = &biz.Member{ConversationID: conversationID, UserID: uid, ReadSeq: maxSeq, JoinedAt: now, UpdatedAt: now}
	}
	return r.AddIgnore(ctx, ms)
}

func (r *memberRepo) Remove(ctx context.Context, conversationID, userID int64) error {
	return r.d.DB(ctx).Where("conversation_id = ? AND user_id = ?", conversationID, userID).Delete(&memberPO{}).Error
}

func (r *memberRepo) ListByUser(ctx context.Context, userID, cursor int64, limit int) ([]*biz.Member, error) {
	var pos []memberPO
	err := r.d.DB(ctx).Where("user_id = ? AND conversation_id > ?", userID, cursor).
		Order("conversation_id").Limit(limit).Find(&pos).Error
	if err != nil {
		return nil, err
	}
	out := make([]*biz.Member, len(pos))
	for i := range pos {
		out[i] = pos[i].toBiz()
	}
	return out, nil
}

func (r *memberRepo) ListOthers(ctx context.Context, conversationIDs []int64, me int64) (map[int64]*biz.Member, error) {
	out := make(map[int64]*biz.Member, len(conversationIDs))
	if len(conversationIDs) == 0 {
		return out, nil
	}
	var pos []memberPO
	err := r.d.DB(ctx).Where("conversation_id IN ? AND user_id <> ?", conversationIDs, me).Find(&pos).Error
	if err != nil {
		return nil, err
	}
	for i := range pos {
		out[pos[i].ConversationID] = pos[i].toBiz()
	}
	return out, nil
}

// AdvanceRead 用条件更新保证已读位置只增不减，并发的两次标记不会互相覆盖成更小的值。
func (r *memberRepo) AdvanceRead(ctx context.Context, conversationID, userID, seq int64, now time.Time) (bool, error) {
	res := r.d.DB(ctx).Model(&memberPO{}).
		Where("conversation_id = ? AND user_id = ? AND read_seq < ?", conversationID, userID, seq).
		Updates(map[string]any{"read_seq": seq, "updated_at": now})
	return res.RowsAffected > 0, res.Error
}
