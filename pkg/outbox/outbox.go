// Package outbox 实现事务 Outbox 的写入端：业务修改和事件在同一个数据库事务中提交，
// 保证“改了数据就一定有事件、没改数据就一定没有事件”。发布端见 publisher.go：
// 读取未发布的事件，按写入顺序投递到 Kafka。
//
// 每个服务的库中都有同构的 outbox_events 表，由各服务的迁移创建：
//
//	CREATE TABLE outbox_events (
//	  id           BIGINT       NOT NULL AUTO_INCREMENT,
//	  topic        VARCHAR(128) NOT NULL,
//	  event_key    VARCHAR(64)  NOT NULL,
//	  event_type   VARCHAR(128) NOT NULL,
//	  payload      BLOB         NOT NULL,
//	  trace_parent VARCHAR(64)  NOT NULL DEFAULT '',  -- 步骤 8 的迁移加入
//	  created_at   DATETIME(3)  NOT NULL,
//	  published_at DATETIME(3)  NULL,
//	  PRIMARY KEY (id),
//	  KEY idx_unpublished (published_at, id)
//	);
package outbox

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"

	"github.com/Mars-08042/MarsChat/pkg/kafka"
)

// Event 是一条待发布的事件。
type Event struct {
	// 自增主键即发布顺序
	ID int64 `gorm:"primaryKey;autoIncrement"`
	// Topic 是 Kafka topic
	Topic string
	// EventKey 是 Kafka 分区键，同一个键的事件进入同一分区，保证顺序
	EventKey string
	// EventType 是 payload 的 protobuf 全名，消费方据此解码
	EventType string
	Payload   []byte
	// TraceParent 是写入时所在 Span 的 W3C traceparent，发布时放进 Kafka 记录头，消费者接着这条 Trace；
	// 写入时没有 Span 则为空
	TraceParent string
	CreatedAt   time.Time
	PublishedAt *time.Time
}

// TableName 指定表名。
func (Event) TableName() string { return "outbox_events" }

// Add 写入一条事件。db 必须是业务修改所在的事务，否则失去 Outbox 的意义；
// ctx 中的 Span 上下文随事件保存，异步投递之后仍在同一条 Trace 上。
func Add(ctx context.Context, db *gorm.DB, topic, key string, msg proto.Message, now time.Time) error {
	payload, err := proto.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal outbox event: %w", err)
	}
	e := &Event{
		Topic:       topic,
		EventKey:    key,
		EventType:   string(msg.ProtoReflect().Descriptor().FullName()),
		Payload:     payload,
		TraceParent: kafka.TraceParent(ctx),
		CreatedAt:   now,
	}
	if err := db.Create(e).Error; err != nil {
		return fmt.Errorf("insert outbox event: %w", err)
	}
	return nil
}
