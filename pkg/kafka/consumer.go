package kafka

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	"github.com/Mars-08042/MarsChat/pkg/conf"
)

const (
	// 每次 poll 最多取的记录数。BlockRebalanceOnPoll 下一批处理完之前不能再平衡，批次不宜过大
	maxPollRecords = 500
	// 单次处理的超时，防止处理器卡死后整个分区停住且没有任何日志
	handleTimeout = 30 * time.Second
	// 提交位点的超时；停机时 ctx 已取消，用独立的超时提交最后一次
	commitTimeout = 10 * time.Second
	// poll 只返回错误时的等待时间，避免 broker 不可用时空转刷日志
	fetchErrorPause = time.Second
)

// Event 是消费到的一条事件。
type Event struct {
	Topic     string
	Partition int32
	Offset    int64
	// Key 是分区键
	Key string
	// Type 是 payload 的 protobuf 全名
	Type string
	// ID 是全局唯一的事件 ID（来源服务加 outbox id），用于消费幂等
	ID      string
	Source  string
	Payload []byte
}

// Handler 处理一条事件。返回普通错误时原地退避重试，直到成功或服务停止；
// 返回 Permanent 包装的错误时记 ERROR 并跳过。
type Handler func(ctx context.Context, e *Event) error

type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

// Permanent 把错误标记为不可重试，例如事件内容本身有问题，重试多少次结果都一样。
func Permanent(err error) error {
	return &permanentError{err: err}
}

// ConsumerConfig 是消费者的参数。
type ConsumerConfig struct {
	// Group 是消费组名，同组的实例分摊分区
	Group  string
	Topics []string
	// FromLatest 为 true 时，新消费组从最新位置开始，适合只关心实时事件的场景（推送）；
	// 默认从最早位置开始，不漏掉消费组创建之前的事件（维护投影、删除缓存）。
	// 已经提交过位点的消费组总是从提交的位点继续。
	FromLatest bool
}

// Consumer 是按事件类型分发的消费组消费者，实现 Kratos 的 transport.Server，随服务启停。
//
// 处理顺序与提交：每次 poll 后按分区内的顺序逐条处理，处理完的记录才提交位点；
// 配合 BlockRebalanceOnPoll，一批处理完并提交之前分区不会被分走，不会在再平衡时提交到别人的分区。
// 处理器失败时原地重试会阻塞所在分区，重试队列与死信队列在重构步骤 5 实现。
type Consumer struct {
	cl       *kgo.Client
	group    string
	handlers map[string]Handler
	logger   *slog.Logger

	retryMin time.Duration
	retryMax time.Duration

	ctx       context.Context
	cancel    context.CancelFunc
	started   atomic.Bool
	done      chan struct{}
	closeOnce sync.Once
}

// NewConsumer 创建消费者。处理器要在服务启动前用 Handle 或 On 注册。
// 返回的 cleanup 用于启动失败时释放客户端；正常停机由 Stop 负责，重复关闭是安全的。
func NewConsumer(c *conf.Kafka, cc ConsumerConfig, logger *slog.Logger) (*Consumer, func(), error) {
	brokers := Brokers(c)
	if len(brokers) == 0 {
		return nil, nil, fmt.Errorf("kafka brokers not configured")
	}
	reset := kgo.NewOffset().AtStart()
	if cc.FromLatest {
		reset = kgo.NewOffset().AtEnd()
	}
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ClientID(cc.Group),
		kgo.ConsumerGroup(cc.Group),
		kgo.ConsumeTopics(cc.Topics...),
		kgo.ConsumeResetOffset(reset),
		kgo.DisableAutoCommit(),
		kgo.BlockRebalanceOnPoll(),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("create kafka consumer %s: %w", cc.Group, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	k := &Consumer{
		cl:       cl,
		group:    cc.Group,
		handlers: make(map[string]Handler),
		logger:   logger.With(slog.String("kafka.group", cc.Group)),
		retryMin: 100 * time.Millisecond,
		retryMax: 5 * time.Second,
		ctx:      ctx,
		cancel:   cancel,
		done:     make(chan struct{}),
	}
	return k, k.close, nil
}

// Handle 注册事件类型对应的处理器。
func (c *Consumer) Handle(eventType string, h Handler) {
	c.handlers[eventType] = h
}

// On 按 protobuf 消息类型注册处理器，负责解码；解码失败视为不可重试。
func On[T proto.Message](c *Consumer, h func(ctx context.Context, e *Event, msg T) error) {
	var zero T
	mt := zero.ProtoReflect().Type()
	c.Handle(string(mt.Descriptor().FullName()), func(ctx context.Context, e *Event) error {
		msg := mt.New().Interface().(T)
		if err := proto.Unmarshal(e.Payload, msg); err != nil {
			return Permanent(fmt.Errorf("decode %s: %w", e.Type, err))
		}
		return h(ctx, e, msg)
	})
}

// Start 持续消费直到 Stop 被调用。Kratos 传入的 ctx 在停机时不会取消，所以用自己的 ctx。
func (c *Consumer) Start(context.Context) error {
	c.started.Store(true)
	defer close(c.done)
	c.logger.Info("kafka consumer started")
	c.run(c.ctx)
	return nil
}

// Stop 停止消费：等待当前事件处理完、提交已处理的位点，然后离开消费组。
func (c *Consumer) Stop(ctx context.Context) error {
	c.cancel()
	if c.started.Load() {
		select {
		case <-c.done:
		case <-ctx.Done():
			c.logger.Warn("kafka consumer stop timed out")
		}
	}
	c.close()
	return nil
}

func (c *Consumer) close() {
	c.closeOnce.Do(func() {
		c.cancel()
		c.cl.CloseAllowingRebalance()
	})
}

func (c *Consumer) run(ctx context.Context) {
	for ctx.Err() == nil {
		fetches := c.cl.PollRecords(ctx, maxPollRecords)
		if fetches.IsClientClosed() {
			return
		}
		fetchFailed := false
		fetches.EachError(func(topic string, partition int32, err error) {
			if errors.Is(err, context.Canceled) {
				return
			}
			fetchFailed = true
			c.logger.Warn("kafka fetch failed", slog.String("topic", topic),
				slog.Int64("partition", int64(partition)), slog.Any("error", err))
		})

		var processed []*kgo.Record
		aborted := false
		fetches.EachRecord(func(r *kgo.Record) {
			if aborted {
				return
			}
			if !c.process(ctx, r) {
				aborted = true
				return
			}
			processed = append(processed, r)
		})
		c.commit(processed)
		c.cl.AllowRebalance()
		if aborted {
			return
		}
		if fetchFailed && fetches.NumRecords() == 0 {
			select {
			case <-ctx.Done():
			case <-time.After(fetchErrorPause):
			}
		}
	}
}

// commit 提交已处理记录的位点。只提交处理完的记录：停机时中断的那条及之后的记录不提交，重启后重新处理。
func (c *Consumer) commit(processed []*kgo.Record) {
	if len(processed) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), commitTimeout)
	defer cancel()
	if err := c.cl.CommitRecords(ctx, processed...); err != nil {
		// 提交失败不影响正确性：下次从旧位点重新处理，由消费幂等兜底
		c.logger.Warn("kafka commit failed", slog.Int("records", len(processed)), slog.Any("error", err))
	}
}

// process 处理一条记录，返回 false 表示服务正在停止、记录没有处理完。
func (c *Consumer) process(ctx context.Context, r *kgo.Record) bool {
	e := &Event{
		Topic:     r.Topic,
		Partition: r.Partition,
		Offset:    r.Offset,
		Key:       string(r.Key),
		Type:      Header(r, HeaderEventType),
		ID:        Header(r, HeaderEventID),
		Source:    Header(r, HeaderSource),
		Payload:   r.Value,
	}
	h, ok := c.handlers[e.Type]
	if !ok {
		// 本服务不关心的事件类型，或者以后新增的类型
		return true
	}
	backoff := c.retryMin
	for attempt := 1; ; attempt++ {
		err := c.call(ctx, h, e)
		if err == nil {
			return true
		}
		attrs := []any{
			slog.String("event.id", e.ID), slog.String("event.type", e.Type),
			slog.String("topic", e.Topic), slog.Int64("partition", int64(e.Partition)),
			slog.Int64("offset", e.Offset), slog.Any("error", err),
		}
		var perm *permanentError
		if errors.As(err, &perm) {
			c.logger.Error("skip kafka event after permanent failure", attrs...)
			return true
		}
		if ctx.Err() != nil {
			return false
		}
		c.logger.Warn("handle kafka event failed, retrying",
			append(attrs, slog.Int("attempt", attempt), slog.Duration("backoff", backoff))...)
		select {
		case <-ctx.Done():
			return false
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, c.retryMax)
	}
}

// call 执行处理器。panic 说明代码有缺陷，重试也一样会 panic，按不可重试处理，避免整个分区卡死。
func (c *Consumer) call(ctx context.Context, h Handler, e *Event) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = Permanent(fmt.Errorf("handler panic: %v\n%s", p, debug.Stack()))
		}
	}()
	hctx, cancel := context.WithTimeout(ctx, handleTimeout)
	defer cancel()
	return h(hctx, e)
}
