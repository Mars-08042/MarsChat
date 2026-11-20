// Package server 创建 delivery 的 gRPC 服务器和 Kafka 消费者。
package server

import (
	"context"
	"log/slog"

	"github.com/go-kratos/kratos/v3/transport/grpc"
	"github.com/google/wire"
	"google.golang.org/protobuf/proto"

	accessv1 "github.com/Mars-08042/MarsChat/api/access/v1"
	accountv1 "github.com/Mars-08042/MarsChat/api/account/v1"
	messagev1 "github.com/Mars-08042/MarsChat/api/message/v1"
	socialv1 "github.com/Mars-08042/MarsChat/api/social/v1"
	"github.com/Mars-08042/MarsChat/app/delivery/internal/biz"
	"github.com/Mars-08042/MarsChat/pkg/conf"
	"github.com/Mars-08042/MarsChat/pkg/kafka"
)

// ProviderSet 是 server 层的依赖提供者。
var ProviderSet = wire.NewSet(NewGRPCServer, NewEventConsumer)

// NewGRPCServer 创建 gRPC 服务器。delivery 没有业务接口，服务器只提供 Kratos 内置的健康检查，并用于注册到 Consul。
func NewGRPCServer(c *conf.Server) *grpc.Server {
	var opts []grpc.ServerOption
	if addr := c.GetGrpc().GetAddr(); addr != "" {
		opts = append(opts, grpc.Address(addr))
	}
	if t := c.GetGrpc().GetTimeout(); t != nil {
		opts = append(opts, grpc.Timeout(t.AsDuration()))
	}
	return grpc.NewServer(opts...)
}

// consumerGroup 是 delivery 的消费组。新消费组从最新位置开始：推送只对在线连接有意义，
// 历史消息由客户端补拉，没必要在第一次启动时推送积压的事件。
const consumerGroup = "marschat-delivery"

// NewEventConsumer 消费消息事件（推送）、账号事件（踢连接）和 social 事件（通知）。处理器不返回错误：
// 推送尽力而为，失败只记日志，不阻塞后面的消息（设计 3.8），所以不需要重试队列；解码失败的记录进死信队列。
// social 的 topic 是步骤 5 新增订阅的，消费组在它上面没有提交过位点，同样从最新位置开始。
func NewEventConsumer(c *conf.Kafka, uc *biz.DeliveryUsecase, logger *slog.Logger) (*kafka.Consumer, func(), error) {
	k, cleanup, err := kafka.NewConsumer(c, kafka.ConsumerConfig{
		Group:      consumerGroup,
		Topics:     []string{kafka.TopicMessageEvents, kafka.TopicAccountEvents, kafka.TopicSocialEvents},
		FromLatest: true,
	}, logger)
	if err != nil {
		return nil, nil, err
	}
	kafka.On(k, func(ctx context.Context, e *kafka.Event, ev *messagev1.MessageCreated) error {
		uc.OnMessageCreated(ctx, e.ID, ev)
		return nil
	})
	kafka.On(k, func(ctx context.Context, e *kafka.Event, ev *messagev1.ReadPositionUpdated) error {
		uc.OnReadPositionUpdated(ctx, e.ID, ev)
		return nil
	})
	kafka.On(k, func(ctx context.Context, _ *kafka.Event, ev *accountv1.SessionRevoked) error {
		uc.OnSessionRevoked(ctx, ev)
		return nil
	})
	kafka.On(k, func(ctx context.Context, _ *kafka.Event, ev *accountv1.UserBanned) error {
		uc.OnUserBanned(ctx, ev)
		return nil
	})
	onSocial(k, uc, func(ev *socialv1.FriendRequestCreated) *accessv1.Notification {
		return &accessv1.Notification{Body: &accessv1.Notification_FriendRequestCreated{FriendRequestCreated: ev}}
	})
	onSocial(k, uc, func(ev *socialv1.FriendAdded) *accessv1.Notification {
		return &accessv1.Notification{Body: &accessv1.Notification_FriendAdded{FriendAdded: ev}}
	})
	onSocial(k, uc, func(ev *socialv1.FriendDeleted) *accessv1.Notification {
		return &accessv1.Notification{Body: &accessv1.Notification_FriendDeleted{FriendDeleted: ev}}
	})
	onSocial(k, uc, func(ev *socialv1.GroupCreated) *accessv1.Notification {
		return &accessv1.Notification{Body: &accessv1.Notification_GroupCreated{GroupCreated: ev}}
	})
	onSocial(k, uc, func(ev *socialv1.GroupMembersJoined) *accessv1.Notification {
		return &accessv1.Notification{Body: &accessv1.Notification_GroupMembersJoined{GroupMembersJoined: ev}}
	})
	onSocial(k, uc, func(ev *socialv1.GroupMemberLeft) *accessv1.Notification {
		return &accessv1.Notification{Body: &accessv1.Notification_GroupMemberLeft{GroupMemberLeft: ev}}
	})
	onSocial(k, uc, func(ev *socialv1.GroupMemberUpdated) *accessv1.Notification {
		return &accessv1.Notification{Body: &accessv1.Notification_GroupMemberUpdated{GroupMemberUpdated: ev}}
	})
	onSocial(k, uc, func(ev *socialv1.GroupUpdated) *accessv1.Notification {
		return &accessv1.Notification{Body: &accessv1.Notification_GroupUpdated{GroupUpdated: ev}}
	})
	onSocial(k, uc, func(ev *socialv1.GroupDismissed) *accessv1.Notification {
		return &accessv1.Notification{Body: &accessv1.Notification_GroupDismissed{GroupDismissed: ev}}
	})
	onSocial(k, uc, func(ev *socialv1.GroupJoinRequestCreated) *accessv1.Notification {
		return &accessv1.Notification{Body: &accessv1.Notification_GroupJoinRequestCreated{GroupJoinRequestCreated: ev}}
	})
	return k, cleanup, nil
}

// onSocial 注册一种 social 事件：包装成通知帧交给用例推送。
func onSocial[T proto.Message](k *kafka.Consumer, uc *biz.DeliveryUsecase, wrap func(T) *accessv1.Notification) {
	kafka.On(k, func(ctx context.Context, e *kafka.Event, ev T) error {
		n := wrap(ev)
		n.EventId = e.ID
		uc.OnSocialEvent(ctx, n)
		return nil
	})
}
