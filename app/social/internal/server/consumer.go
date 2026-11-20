package server

import (
	"context"
	"log/slog"
	"time"

	"google.golang.org/protobuf/proto"

	v1 "github.com/Mars-08042/MarsChat/api/social/v1"
	"github.com/Mars-08042/MarsChat/app/social/internal/biz"
	"github.com/Mars-08042/MarsChat/pkg/conf"
	"github.com/Mars-08042/MarsChat/pkg/kafka"
)

// cacheConsumerGroup 是 social 消费自己事件的消费组。从最新位置开始：之前的事件已经由事务后的同步删除处理过。
const cacheConsumerGroup = "marschat-social-cache"

// NewCacheEventConsumer 消费 social 自己的群事件，再删一次群资料和成员列表缓存（设计 3.9，TD-25）。
// 事务后的同步删除仍是快速路径；它失败时这里兜底。删除缓存与顺序无关，所以用重试队列：
// 原地尝试 3 次仍失败就转入 marschat.social-cache.retry，10 秒后由重试消费者再删，直到成功，主分区不被阻塞。
func NewCacheEventConsumer(c *conf.Kafka, inv biz.GroupCacheInvalidator, logger *slog.Logger) (*kafka.Consumer, func(), error) {
	k, cleanup, err := kafka.NewConsumer(c, kafka.ConsumerConfig{
		Group:         cacheConsumerGroup,
		Topics:        []string{kafka.TopicSocialEvents},
		FromLatest:    true,
		RetryTopic:    kafka.TopicSocialCacheRetry,
		RetryAttempts: 3,
		RetryDelay:    10 * time.Second,
	}, logger)
	if err != nil {
		return nil, nil, err
	}
	onGroupEvent(k, inv, func(e *v1.GroupCreated) int64 { return e.GetGroup().GetId() })
	onGroupEvent(k, inv, func(e *v1.GroupUpdated) int64 { return e.GetGroup().GetId() })
	onGroupEvent(k, inv, func(e *v1.GroupDismissed) int64 { return e.GetGroupId() })
	onGroupEvent(k, inv, func(e *v1.GroupMembersJoined) int64 { return e.GetGroupId() })
	onGroupEvent(k, inv, func(e *v1.GroupMemberLeft) int64 { return e.GetGroupId() })
	onGroupEvent(k, inv, func(e *v1.GroupMemberUpdated) int64 { return e.GetGroupId() })
	return k, cleanup, nil
}

// onGroupEvent 注册一种群事件：取出群 ID 后删除缓存，返回的错误让消费者重试。
func onGroupEvent[T proto.Message](k *kafka.Consumer, inv biz.GroupCacheInvalidator, groupID func(T) int64) {
	kafka.On(k, func(ctx context.Context, _ *kafka.Event, e T) error {
		id := groupID(e)
		if id <= 0 {
			return nil
		}
		return inv.InvalidateGroup(ctx, id)
	})
}
