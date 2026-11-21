package data

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"

	v1 "github.com/Mars-08042/MarsChat/api/message/v1"
	"github.com/Mars-08042/MarsChat/app/message/internal/biz"
)

// messagePO 对应 messages 表，content 是 message.v1.MessageContent 的 protobuf 编码。
type messagePO struct {
	ConversationID int64 `gorm:"primaryKey;autoIncrement:false"`
	Seq            int64 `gorm:"primaryKey;autoIncrement:false"`
	SenderID       int64
	ClientMsgID    string
	Content        []byte
	CreatedAt      time.Time
}

func (messagePO) TableName() string { return "messages" }

func (p *messagePO) toBiz() (*biz.Message, error) {
	content := &v1.MessageContent{}
	if err := proto.Unmarshal(p.Content, content); err != nil {
		return nil, fmt.Errorf("decode message %d/%d: %w", p.ConversationID, p.Seq, err)
	}
	return &biz.Message{
		ConversationID: p.ConversationID, Seq: p.Seq, SenderID: p.SenderID,
		ClientMsgID: p.ClientMsgID, Content: content, CreatedAt: p.CreatedAt,
	}, nil
}

func toBizMessages(pos []messagePO) ([]*biz.Message, error) {
	out := make([]*biz.Message, len(pos))
	for i := range pos {
		m, err := pos[i].toBiz()
		if err != nil {
			return nil, err
		}
		out[i] = m
	}
	return out, nil
}

type messageRepo struct{ d *Data }

// NewMessageRepo 创建消息存储。
func NewMessageRepo(d *Data) biz.MessageRepo {
	return &messageRepo{d: d}
}

func (r *messageRepo) Create(ctx context.Context, m *biz.Message) error {
	content, err := proto.Marshal(m.Content)
	if err != nil {
		return fmt.Errorf("encode message content: %w", err)
	}
	po := &messagePO{
		ConversationID: m.ConversationID, Seq: m.Seq, SenderID: m.SenderID,
		ClientMsgID: m.ClientMsgID, Content: content, CreatedAt: m.CreatedAt,
	}
	err = r.d.DB(ctx).Create(po).Error
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		// 主键 (conversation_id, seq) 在会话行锁下分配，不会重复；能撞上的只有幂等键 uk_client
		return biz.ErrDuplicate
	}
	return err
}

func (r *messageRepo) FindByClientID(ctx context.Context, conversationID, senderID int64, clientMsgID string) (*biz.Message, error) {
	var po messagePO
	err := r.d.DB(ctx).Where("conversation_id = ? AND sender_id = ? AND client_msg_id = ?", conversationID, senderID, clientMsgID).
		Take(&po).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, biz.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return po.toBiz()
}

func (r *messageRepo) ListBefore(ctx context.Context, conversationID, beforeSeq int64, limit int) ([]*biz.Message, error) {
	var pos []messagePO
	err := r.d.DB(ctx).Where("conversation_id = ? AND seq < ?", conversationID, beforeSeq).
		Order("seq DESC").Limit(limit).Find(&pos).Error
	if err != nil {
		return nil, err
	}
	return toBizMessages(pos)
}

func (r *messageRepo) ListAfter(ctx context.Context, conversationID, afterSeq int64, limit int) ([]*biz.Message, error) {
	var pos []messagePO
	err := r.d.DB(ctx).Where("conversation_id = ? AND seq > ?", conversationID, afterSeq).
		Order("seq").Limit(limit).Find(&pos).Error
	if err != nil {
		return nil, err
	}
	return toBizMessages(pos)
}

// BatchGet 按 (conversation_id, seq) 主键批量读取，用行构造器 IN 生成主键上的点查询。
func (r *messageRepo) BatchGet(ctx context.Context, keys []biz.SeqKey) (map[biz.SeqKey]*biz.Message, error) {
	out := make(map[biz.SeqKey]*biz.Message, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	pairs := make([][]any, len(keys))
	for i, k := range keys {
		pairs[i] = []any{k.ConversationID, k.Seq}
	}
	var pos []messagePO
	if err := r.d.DB(ctx).Where("(conversation_id, seq) IN ?", pairs).Find(&pos).Error; err != nil {
		return nil, err
	}
	for i := range pos {
		m, err := pos[i].toBiz()
		if err != nil {
			return nil, err
		}
		out[biz.SeqKey{ConversationID: m.ConversationID, Seq: m.Seq}] = m
	}
	return out, nil
}
