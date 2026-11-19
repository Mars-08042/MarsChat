package data

import (
	"context"
	"errors"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/Mars-08042/MarsChat/app/social/internal/biz"
	"github.com/Mars-08042/MarsChat/pkg/outbox"
)

type eventRepo struct{ d *Data }

// NewEventRepo 创建 Outbox 事件存储。
func NewEventRepo(d *Data) biz.EventRepo {
	return &eventRepo{d: d}
}

func (r *eventRepo) Add(ctx context.Context, topic, key string, msg proto.Message, now time.Time) error {
	// 不在事务中写事件就失去了 Outbox 的意义，属于编程错误，直接报错
	if !inTx(ctx) {
		return errors.New("outbox event must be written inside a transaction")
	}
	return outbox.Add(r.d.DB(ctx), topic, key, msg, now)
}
