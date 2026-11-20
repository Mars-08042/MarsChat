// Package kafka 封装 franz-go：创建客户端、约定记录头，以及按事件类型分发的消费者。
//
// 事件由各服务的 Outbox 发布器写入（见 pkg/outbox），记录的键是分区键，值是 protobuf 编码的事件，
// 记录头带事件类型、事件 ID 和来源服务。
package kafka

import (
	"fmt"
	"strings"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/Mars-08042/MarsChat/pkg/conf"
)

// 记录头的键。
const (
	// HeaderEventType 是 payload 的 protobuf 全名，消费方据此解码
	HeaderEventType = "event_type"
	// HeaderEventID 是 "{来源服务}:{outbox id}"，全局唯一，用于消费幂等
	HeaderEventID = "event_id"
	// HeaderSource 是产生事件的服务名
	HeaderSource = "source"

	// 写入死信队列时追加的头：消费组、原记录的位置和失败原因
	HeaderDLQGroup     = "x-dlq-group"
	HeaderDLQTopic     = "x-dlq-topic"
	HeaderDLQPartition = "x-dlq-partition"
	HeaderDLQOffset    = "x-dlq-offset"
	HeaderDLQError     = "x-dlq-error"

	// HeaderRetryNotBefore 是重试记录最早可以处理的时间，Unix 毫秒
	HeaderRetryNotBefore = "x-retry-not-before"
	// HeaderRetryFrom 是转入重试队列前的原记录位置，格式为 topic/partition/offset
	HeaderRetryFrom = "x-retry-from"
)

// 各服务的事件 topic。topic 由 compose 的 kafka-init 任务创建，broker 关闭了自动创建。
const (
	TopicAccountEvents = "marschat.account.events"
	TopicSocialEvents  = "marschat.social.events"
	TopicMessageEvents = "marschat.message.events"

	// TopicDLQ 是所有消费组共用的死信队列，保留 14 天
	TopicDLQ = "marschat.dlq"
	// TopicSocialCacheRetry 是消费组 marschat-social-cache 的重试队列
	TopicSocialCacheRetry = "marschat.social-cache.retry"
)

// Brokers 解析逗号分隔的 broker 列表。
func Brokers(c *conf.Kafka) []string {
	var out []string
	for _, b := range strings.Split(c.GetBrokers(), ",") {
		if b = strings.TrimSpace(b); b != "" {
			out = append(out, b)
		}
	}
	return out
}

// NewProducer 创建只用于生产的客户端。franz-go 默认 acks=all 并开启幂等生产者，
// 同一分区内的记录不会因重试而重复或乱序。clientID 用服务名，便于在 broker 侧区分来源。
func NewProducer(c *conf.Kafka, clientID string) (*kgo.Client, func(), error) {
	brokers := Brokers(c)
	if len(brokers) == 0 {
		return nil, nil, fmt.Errorf("kafka brokers not configured")
	}
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ClientID(clientID),
		// 单条记录的投递上限：发布器的单批超时更短，这里只是兜底，避免记录无限期挂在缓冲区
		kgo.RecordDeliveryTimeout(30*time.Second),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("create kafka producer: %w", err)
	}
	return cl, cl.Close, nil
}

// Header 返回记录头中 key 对应的值，不存在时返回空字符串。
func Header(r *kgo.Record, key string) string {
	for _, h := range r.Headers {
		if h.Key == key {
			return string(h.Value)
		}
	}
	return ""
}
