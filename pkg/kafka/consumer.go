package kafka

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strconv"
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
	// 写入死信队列或重试队列的单次超时
	writeTimeout = 10 * time.Second
	// 同一条记录连续失败这么多次之后改记 ERROR：分区可能被长时间阻塞，需要人工关注
	escalateAfter = 10
	// 升级为 ERROR 之后，同一条记录的失败日志最多每隔这么久记一次
	escalatedLogEvery = time.Minute
	// 死信记录中失败原因的最大字节数，panic 的堆栈可能很长
	maxErrorHeader = 4096

	defaultRetryAttempts = 3
	defaultRetryDelay    = 10 * time.Second
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

// Handler 处理一条事件。返回普通错误表示临时失败，按消费者的配置重试（见 ConsumerConfig.RetryTopic）；
// 返回 Permanent 包装的错误表示重试也没有用，记录写入死信队列后跳过。
type Handler func(ctx context.Context, e *Event) error

type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

// Permanent 把错误标记为不可重试，例如事件内容本身有问题，重试多少次结果都一样。
func Permanent(err error) error {
	return &permanentError{err: err}
}

// ConsumerConfig 是消费者的参数。重试与死信的参数由各服务在代码中设置，不是限额，不做热更新。
type ConsumerConfig struct {
	// Group 是消费组名，同组的实例分摊分区
	Group  string
	Topics []string
	// FromLatest 为 true 时，新消费组从最新位置开始，适合只关心实时事件的场景（推送）；
	// 默认从最早位置开始，不漏掉消费组创建之前的事件（维护投影、删除缓存）。
	// 已经提交过位点的消费组总是从提交的位点继续。
	FromLatest bool
	// RetryTopic 非空时启用重试队列，只能用于不要求顺序的消费者：临时错误原地尝试 RetryAttempts 次仍失败，
	// 就把记录写入 RetryTopic 并提交原位点，主分区不被阻塞。重试消费者（消费组 Group+"-retry"）
	// 在 RetryDelay 之后用同样的处理器再处理，临时错误原地无限重试，只阻塞重试队列。
	//
	// 为空时临时错误原地无限重试，同一分区后面的记录等它成功才处理，适合要求按顺序生效的消费者（投影）。
	RetryTopic string
	// RetryAttempts 是转入重试队列前的原地尝试次数，默认 3
	RetryAttempts int
	// RetryDelay 是重试队列中每条记录的固定延迟，默认 10 秒
	RetryDelay time.Duration
}

// Consumer 是按事件类型分发的消费组消费者，实现 Kratos 的 transport.Server，随服务启停。
//
// 处理顺序与提交：每次 poll 后按分区内的顺序逐条处理，处理完的记录才提交位点；
// 配合 BlockRebalanceOnPoll，一批处理完并提交之前分区不会被分走，不会在再平衡时提交到别人的分区。
//
// 失败处理：Permanent 错误、解码失败和 panic 的记录写入死信队列（TopicDLQ）后提交位点；
// 写入失败时不提交，原地重试写入，记录不会凭空消失。临时错误见 ConsumerConfig.RetryTopic。
type Consumer struct {
	cl       *kgo.Client
	group    string
	handlers map[string]Handler
	logger   *slog.Logger
	now      func() time.Time
	// write 把记录写入死信队列或重试队列，测试中替换
	write func(ctx context.Context, r *kgo.Record) error

	retryMin time.Duration
	retryMax time.Duration

	// 主消费者：retryTopic 非空时，原地尝试 retryAttempts 次后把记录转入 retryTopic
	retryTopic    string
	retryAttempts int
	retryDelay    time.Duration
	// delayed 为 true 表示这是重试消费者：按记录头的时间延后处理，最多等 retryDelay
	delayed bool
	// retry 是主消费者持有的重试消费者，与主消费者共用处理器，一起启停
	retry *Consumer

	ctx       context.Context
	cancel    context.CancelFunc
	started   atomic.Bool
	done      chan struct{}
	closeOnce sync.Once
}

// NewConsumer 创建消费者。处理器要在服务启动前用 Handle 或 On 注册。
// 返回的 cleanup 用于启动失败时释放客户端；正常停机由 Stop 负责，重复关闭是安全的。
func NewConsumer(c *conf.Kafka, cc ConsumerConfig, logger *slog.Logger) (*Consumer, func(), error) {
	reset := kgo.NewOffset().AtStart()
	if cc.FromLatest {
		reset = kgo.NewOffset().AtEnd()
	}
	k, err := newConsumer(c, cc.Group, cc.Topics, reset, logger)
	if err != nil {
		return nil, nil, err
	}
	if cc.RetryTopic == "" {
		return k, k.close, nil
	}
	k.retryTopic = cc.RetryTopic
	k.retryAttempts = cc.RetryAttempts
	if k.retryAttempts <= 0 {
		k.retryAttempts = defaultRetryAttempts
	}
	k.retryDelay = cc.RetryDelay
	if k.retryDelay <= 0 {
		k.retryDelay = defaultRetryDelay
	}
	// 重试队列总是从最早位置开始：重试消费组第一次启动之前写入的记录也要处理
	r, err := newConsumer(c, cc.Group+"-retry", []string{cc.RetryTopic}, kgo.NewOffset().AtStart(), logger)
	if err != nil {
		k.close()
		return nil, nil, err
	}
	r.delayed = true
	r.retryDelay = k.retryDelay
	r.handlers = k.handlers
	k.retry = r
	return k, k.close, nil
}

func newConsumer(c *conf.Kafka, group string, topics []string, reset kgo.Offset, logger *slog.Logger) (*Consumer, error) {
	brokers := Brokers(c)
	if len(brokers) == 0 {
		return nil, fmt.Errorf("kafka brokers not configured")
	}
	opts := []kgo.Opt{
		kgo.SeedBrokers(brokers...),
		kgo.ClientID(group),
		kgo.ConsumerGroup(group),
		kgo.ConsumeTopics(topics...),
		kgo.ConsumeResetOffset(reset),
		kgo.DisableAutoCommit(),
		kgo.BlockRebalanceOnPoll(),
	}
	if d := c.GetSessionTimeout(); d != nil {
		opts = append(opts, kgo.SessionTimeout(d.AsDuration()))
	}
	cl, err := kgo.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("create kafka consumer %s: %w", group, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Consumer{
		cl:       cl,
		group:    group,
		handlers: make(map[string]Handler),
		logger:   logger.With(slog.String("kafka.group", group)),
		now:      time.Now,
		// 同一个客户端既消费也生产，默认 acks=all 与幂等生产
		write:    func(ctx context.Context, r *kgo.Record) error { return cl.ProduceSync(ctx, r).FirstErr() },
		retryMin: 100 * time.Millisecond,
		retryMax: 5 * time.Second,
		ctx:      ctx,
		cancel:   cancel,
		done:     make(chan struct{}),
	}, nil
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
func (c *Consumer) Start(ctx context.Context) error {
	if c.retry != nil {
		go func() { _ = c.retry.Start(ctx) }()
	}
	c.started.Store(true)
	defer close(c.done)
	c.logger.Info("kafka consumer started")
	c.run(c.ctx)
	return nil
}

// Stop 停止消费：等待当前事件处理完、提交已处理的位点，然后离开消费组。重试消费者同时停止。
func (c *Consumer) Stop(ctx context.Context) error {
	var wg sync.WaitGroup
	if c.retry != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = c.retry.Stop(ctx)
		}()
	}
	c.cancel()
	if c.started.Load() {
		select {
		case <-c.done:
		case <-ctx.Done():
			c.logger.Warn("kafka consumer stop timed out")
		}
	}
	c.closeClient()
	wg.Wait()
	return nil
}

// close 释放主消费者和重试消费者的客户端。
func (c *Consumer) close() {
	c.closeClient()
	if c.retry != nil {
		c.retry.closeClient()
	}
}

func (c *Consumer) closeClient() {
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
	if c.delayed && !c.waitUntilDue(ctx, r) {
		return false
	}
	backoff := c.retryMin
	var lastEscalated time.Time
	for attempt := 1; ; attempt++ {
		err := c.call(ctx, h, e)
		if err == nil {
			return true
		}
		attrs := []any{
			slog.String("event.id", e.ID), slog.String("event.type", e.Type),
			slog.String("topic", e.Topic), slog.Int64("partition", int64(e.Partition)),
			slog.Int64("offset", e.Offset), slog.Int("attempt", attempt), slog.Any("error", err),
		}
		var perm *permanentError
		if errors.As(err, &perm) {
			c.logger.Error("kafka event failed permanently, moving to dead letter topic", attrs...)
			return c.writeUntilDone(ctx, c.deadLetter(r, err), "dead letter")
		}
		if ctx.Err() != nil {
			return false
		}
		if c.retryTopic != "" && attempt >= c.retryAttempts {
			werr := c.writeOnce(ctx, c.retryRecord(r))
			if werr == nil {
				c.logger.Warn("kafka event moved to retry topic", append(attrs, slog.String("retry_topic", c.retryTopic))...)
				return true
			}
			if ctx.Err() != nil {
				return false
			}
			// 写不进重试队列时留在原地继续重试，下一次失败再尝试转入
			c.logger.Warn("write retry topic failed", slog.String("retry_topic", c.retryTopic), slog.Any("error", werr))
		}
		switch {
		case attempt < escalateAfter:
			c.logger.Warn("handle kafka event failed, retrying", append(attrs, slog.Duration("backoff", backoff))...)
		case attempt == escalateAfter || c.now().Sub(lastEscalated) >= escalatedLogEvery:
			c.logger.Error("kafka event keeps failing, partition blocked", attrs...)
			lastEscalated = c.now()
		}
		if !c.sleep(ctx, backoff) {
			return false
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

// waitUntilDue 等到重试记录头中的时间，返回 false 表示等待期间服务停止。
// 头缺失或无法解析时立即处理；最多等 retryDelay，避免时钟偏差让一条记录挡住整个分区。
func (c *Consumer) waitUntilDue(ctx context.Context, r *kgo.Record) bool {
	ms, err := strconv.ParseInt(Header(r, HeaderRetryNotBefore), 10, 64)
	if err != nil {
		return true
	}
	d := min(time.UnixMilli(ms).Sub(c.now()), c.retryDelay)
	if d <= 0 {
		return true
	}
	return c.sleep(ctx, d)
}

func (c *Consumer) sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// deadLetter 构造死信记录：原始的键、值和头，加上消费组、原位置和失败原因。
func (c *Consumer) deadLetter(r *kgo.Record, cause error) *kgo.Record {
	msg := cause.Error()
	if len(msg) > maxErrorHeader {
		msg = msg[:maxErrorHeader]
	}
	headers := append(cloneHeaders(r.Headers),
		kgo.RecordHeader{Key: HeaderDLQGroup, Value: []byte(c.group)},
		kgo.RecordHeader{Key: HeaderDLQTopic, Value: []byte(r.Topic)},
		kgo.RecordHeader{Key: HeaderDLQPartition, Value: []byte(strconv.FormatInt(int64(r.Partition), 10))},
		kgo.RecordHeader{Key: HeaderDLQOffset, Value: []byte(strconv.FormatInt(r.Offset, 10))},
		kgo.RecordHeader{Key: HeaderDLQError, Value: []byte(msg)},
	)
	return &kgo.Record{Topic: TopicDLQ, Key: r.Key, Value: r.Value, Headers: headers}
}

// retryRecord 构造重试记录：原始的键、值和头，加上最早处理时间和原位置。键不变，同一个键仍进同一个分区。
func (c *Consumer) retryRecord(r *kgo.Record) *kgo.Record {
	notBefore := c.now().Add(c.retryDelay).UnixMilli()
	from := fmt.Sprintf("%s/%d/%d", r.Topic, r.Partition, r.Offset)
	headers := append(cloneHeaders(r.Headers),
		kgo.RecordHeader{Key: HeaderRetryNotBefore, Value: []byte(strconv.FormatInt(notBefore, 10))},
		kgo.RecordHeader{Key: HeaderRetryFrom, Value: []byte(from)},
	)
	return &kgo.Record{Topic: c.retryTopic, Key: r.Key, Value: r.Value, Headers: headers}
}

func (c *Consumer) writeOnce(ctx context.Context, r *kgo.Record) error {
	wctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return c.write(wctx, r)
}

// writeUntilDone 写入成功才返回 true；失败时退避重试，不提交原位点，返回 false 表示服务正在停止。
func (c *Consumer) writeUntilDone(ctx context.Context, r *kgo.Record, what string) bool {
	backoff := c.retryMin
	for {
		err := c.writeOnce(ctx, r)
		if err == nil {
			return true
		}
		if ctx.Err() != nil {
			return false
		}
		c.logger.Warn("write "+what+" failed, retrying", slog.String("topic", r.Topic),
			slog.Duration("backoff", backoff), slog.Any("error", err))
		if !c.sleep(ctx, backoff) {
			return false
		}
		backoff = min(backoff*2, c.retryMax)
	}
}

// cloneHeaders 复制记录头，转发时去掉上一次重试或死信追加的头，避免重复。
func cloneHeaders(hs []kgo.RecordHeader) []kgo.RecordHeader {
	out := make([]kgo.RecordHeader, 0, len(hs)+5)
	for _, h := range hs {
		switch h.Key {
		case HeaderRetryNotBefore, HeaderRetryFrom,
			HeaderDLQGroup, HeaderDLQTopic, HeaderDLQPartition, HeaderDLQOffset, HeaderDLQError:
			continue
		}
		out = append(out, h)
	}
	return out
}
