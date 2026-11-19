package server

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	socialv1 "github.com/Mars-08042/MarsChat/api/social/v1"
	"github.com/Mars-08042/MarsChat/app/message/internal/biz"
	"github.com/Mars-08042/MarsChat/pkg/conf"
	"github.com/Mars-08042/MarsChat/pkg/kafka"
)

// consumerGroup 是 message 消费 social 事件的消费组。新消费组从最早的事件开始，
// 不漏掉 message 第一次启动之前就已经建好的群。
const consumerGroup = "marschat-message"

// NewSocialEventConsumer 消费 social 的群事件，维护群会话的成员（设计 3.4）。
// 处理失败（例如数据库不可用）时原地重试：成员投影必须按顺序生效，不能跳过。
func NewSocialEventConsumer(c *conf.Kafka, uc *biz.MembershipUsecase, logger *slog.Logger) (*kafka.Consumer, func(), error) {
	k, cleanup, err := kafka.NewConsumer(c, kafka.ConsumerConfig{
		Group:  consumerGroup,
		Topics: []string{kafka.TopicSocialEvents},
	}, logger)
	if err != nil {
		return nil, nil, err
	}
	kafka.On(k, func(ctx context.Context, _ *kafka.Event, ev *socialv1.GroupCreated) error {
		g := ev.GetGroup()
		if g.GetId() <= 0 {
			return kafka.Permanent(fmt.Errorf("GroupCreated without group id"))
		}
		return uc.OnGroupCreated(ctx, g.GetId(), ev.GetMemberIds(), eventTime(g.GetCreatedAt()))
	})
	kafka.On(k, func(ctx context.Context, _ *kafka.Event, ev *socialv1.GroupMembersJoined) error {
		if ev.GetGroupId() <= 0 {
			return kafka.Permanent(fmt.Errorf("GroupMembersJoined without group id"))
		}
		return uc.OnMembersJoined(ctx, ev.GetGroupId(), ev.GetUserIds(), eventTime(ev.GetJoinedAt()))
	})
	kafka.On(k, func(ctx context.Context, _ *kafka.Event, ev *socialv1.GroupMemberLeft) error {
		return uc.OnMemberLeft(ctx, ev.GetGroupId(), ev.GetUserId())
	})
	return k, cleanup, nil
}

// eventTime 取事件中的时间，缺失时用当前时间。
func eventTime(ts *timestamppb.Timestamp) time.Time {
	if ts == nil {
		return time.Now()
	}
	return ts.AsTime()
}
