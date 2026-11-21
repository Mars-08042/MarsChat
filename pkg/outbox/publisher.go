package outbox

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/twmb/franz-go/pkg/kgo"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"gorm.io/gorm"

	"github.com/Mars-08042/MarsChat/pkg/conf"
	"github.com/Mars-08042/MarsChat/pkg/kafka"
	"github.com/Mars-08042/MarsChat/pkg/telemetry"
)

// 指标，标签 source 为服务名：
//   - marschat_outbox_published_total{result}：ok 为发出并标记的事件数，error 为发布失败的批次中的事件数（重试会重复计数）；
//   - marschat_outbox_publish_delay_seconds：事件从写入 Outbox 到发出的时长，Kafka 不可用、租约切换时会升高。
var (
	publishedCounter = telemetry.Int64Counter("marschat_outbox_published", "Outbox 事件发布数，按结果区分")
	publishDelay     = telemetry.SecondsHistogram("marschat_outbox_publish_delay_seconds", "Outbox 事件从写入到发出的时长")
)

const (
	// leaseTTL 是发布租约的有效期。续期间隔远小于它，一次 Redis 抖动不会丢掉租约
	leaseTTL      = 10 * time.Second
	renewInterval = 3 * time.Second
	// produceTimeout 是单批发布的超时。发布前要求本地认定的租约剩余时间不少于它，
	// 这样一批发布不会跨过租约到期，降低两个实例同时发布的可能
	produceTimeout = 5 * time.Second
	// 失败后的退避区间
	minBackoff = time.Second
	maxBackoff = 30 * time.Second
	// 清理已发布事件的周期和每批行数
	cleanupInterval = time.Hour
	cleanupBatch    = 1000
	cleanupMaxLoops = 20
	leaseKeyPrefix  = "outbox:lease:"
	// wakeChannelPrefix 是跨实例唤醒的 Redis 频道前缀，见 Notify
	wakeChannelPrefix = "outbox:wake:"
	// remoteWakeTimeout 是发出一次跨实例唤醒的超时；唤醒丢了只是退回轮询，不重试
	remoteWakeTimeout = time.Second
)

// 续期与释放都先确认持有者是自己，避免租约过期后被别的实例拿走时误续、误删。
var (
	renewScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('PEXPIRE', KEYS[1], ARGV[2])
end
return 0`)
	releaseScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0`)
)

// Options 是发布器的参数。
type Options struct {
	// Source 是服务名，用于租约键、记录头 source 和事件 ID 前缀
	Source string
	// Owner 是实例 ID，标识租约持有者
	Owner        string
	PollInterval time.Duration
	BatchSize    int
	Retention    time.Duration
}

type store interface {
	fetch(ctx context.Context, limit int) ([]Event, error)
	markPublished(ctx context.Context, ids []int64, at time.Time) error
	deletePublished(ctx context.Context, before time.Time, limit int) (int64, error)
}

type producer interface {
	produce(ctx context.Context, events []Event) error
}

// Publisher 把 outbox_events 中未发布的事件按写入顺序投递到 Kafka，实现 Kratos 的 transport.Server。
//
// 每个服务同一时刻只有一个实例在发布：实例之间用 Redis 租约 outbox:lease:{服务名} 互斥。
// 这样同一个键的事件按 id 顺序发出，也不需要在持有行锁的事务里调用 Kafka。
// 发布后、标记前崩溃会重复发布，消费方需要幂等。
//
// 写事件的事务可能提交在没有租约的实例上，这时由 Notify 经 Redis 频道 outbox:wake:{服务名}
// 唤醒持有租约的实例；频道不可用时退回按 PollInterval 轮询。
type Publisher struct {
	store    store
	producer producer
	rdb      redis.UniversalClient
	opts     Options
	logger   *slog.Logger
	now      func() time.Time

	wake chan struct{}
	// remoteWake 合并待发出的跨实例唤醒，由 forwardWakes 协程发到 Redis 频道
	remoteWake chan struct{}
	// leader 是发布协程最近一次确认的租约状态，供 Notify 在业务协程中判断是否需要跨实例唤醒
	leader atomic.Bool

	// 以下字段只在发布协程中读写
	leaseUntil time.Time
	lastRenew  time.Time
	backoff    time.Duration
	retryAt    time.Time

	ctx     context.Context
	cancel  context.CancelFunc
	started atomic.Bool
	done    chan struct{}
	once    sync.Once

	// 预先构造的指标标签
	mOK, mErr metric.AddOption
	mDelay    metric.RecordOption
}

// New 按配置创建 Kafka 生产者和发布器。source 是服务名，owner 是实例 ID；
// 返回的 cleanup 关闭生产者，要在发布器停止之后调用（wire 按依赖的逆序执行 cleanup）。
func New(db *gorm.DB, rdb redis.UniversalClient, kc *conf.Kafka, oc *conf.Outbox, source, owner string, logger *slog.Logger) (*Publisher, func(), error) {
	cl, cleanup, err := kafka.NewProducer(kc, source)
	if err != nil {
		return nil, nil, err
	}
	opts := Options{
		Source:       source,
		Owner:        owner,
		PollInterval: oc.GetPollInterval().AsDuration(),
		BatchSize:    int(oc.GetBatchSize()),
		Retention:    oc.GetRetention().AsDuration(),
	}
	return NewPublisher(db, rdb, cl, opts, logger), cleanup, nil
}

// NewPublisher 创建发布器。db 是服务自己的库（含 outbox_events 表），cl 是 Kafka 生产者客户端。
func NewPublisher(db *gorm.DB, rdb redis.UniversalClient, cl *kgo.Client, opts Options, logger *slog.Logger) *Publisher {
	return newPublisher(gormStore{db: db}, kafkaProducer{cl: cl, source: opts.Source}, rdb, opts, logger)
}

func newPublisher(s store, p producer, rdb redis.UniversalClient, opts Options, logger *slog.Logger) *Publisher {
	ctx, cancel := context.WithCancel(context.Background())
	source := attribute.String("source", opts.Source)
	return &Publisher{
		mOK:        metric.WithAttributeSet(attribute.NewSet(source, attribute.String("result", "ok"))),
		mErr:       metric.WithAttributeSet(attribute.NewSet(source, attribute.String("result", "error"))),
		mDelay:     metric.WithAttributeSet(attribute.NewSet(source)),
		store:      s,
		producer:   p,
		rdb:        rdb,
		opts:       opts,
		logger:     logger.With(slog.String("outbox.source", opts.Source)),
		now:        time.Now,
		wake:       make(chan struct{}, 1),
		remoteWake: make(chan struct{}, 1),
		ctx:        ctx,
		cancel:     cancel,
		done:       make(chan struct{}),
	}
}

// Notify 唤醒发布器立即检查新事件。在写了事件的事务提交后调用，不阻塞；
// 已有待处理的唤醒时直接返回，多次提交合并成一次检查。
// 本实例没有持有租约时，事件要由另一个实例发布，同时经 Redis 频道唤醒它；
// 否则只能等它下一轮轮询，多实例下推送延迟会多出最多一个轮询间隔。
func (p *Publisher) Notify() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
	if p.leader.Load() {
		return
	}
	select {
	case p.remoteWake <- struct{}{}:
	default:
	}
}

func (p *Publisher) wakeChannel() string {
	return wakeChannelPrefix + p.opts.Source
}

// forwardWakes 把合并后的跨实例唤醒发到 Redis 频道，直到发布器停止。
func (p *Publisher) forwardWakes(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.remoteWake:
		}
		pctx, cancel := context.WithTimeout(ctx, remoteWakeTimeout)
		err := p.rdb.Publish(pctx, p.wakeChannel(), p.opts.Owner).Err()
		cancel()
		if err != nil && ctx.Err() == nil {
			// 唤醒丢失只会退回轮询；Redis 故障期间每次提交都会走到这里，所以只记 DEBUG
			p.logger.Debug("publish outbox wake-up failed", slog.Any("error", err))
		}
	}
}

// Start 持续发布直到 Stop 被调用。
func (p *Publisher) Start(context.Context) error {
	p.started.Store(true)
	defer close(p.done)
	p.logger.Info("outbox publisher started")
	go p.forwardWakes(p.ctx)
	p.run(p.ctx)
	return nil
}

// Stop 停止发布并释放租约，让其他实例不必等租约过期就能接手。
func (p *Publisher) Stop(ctx context.Context) error {
	p.once.Do(p.cancel)
	if p.started.Load() {
		select {
		case <-p.done:
		case <-ctx.Done():
			p.logger.Warn("outbox publisher stop timed out")
		}
	}
	return nil
}

func (p *Publisher) run(ctx context.Context) {
	poll := time.NewTicker(p.opts.PollInterval)
	renew := time.NewTicker(renewInterval)
	cleanup := time.NewTicker(cleanupInterval)
	defer poll.Stop()
	defer renew.Stop()
	defer cleanup.Stop()
	defer p.releaseLease()

	// 订阅跨实例唤醒。go-redis 在连接断开后自动重新订阅，Redis 不可用期间只是收不到唤醒
	sub := p.rdb.Subscribe(ctx, p.wakeChannel())
	defer func() { _ = sub.Close() }()
	remote := sub.Channel()

	p.maintainLease(ctx)
	p.drain(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-renew.C:
			p.maintainLease(ctx)
		case <-cleanup.C:
			p.cleanup(ctx)
		case <-p.wake:
		case <-remote:
		case <-poll.C:
		}
		p.drain(ctx)
	}
}

// holding 判断本地认定的租约是否有效。本地截止时间取续期请求发出前的时间加 TTL，
// 一定早于 Redis 中键的实际过期时间。
func (p *Publisher) holding() bool {
	return p.now().Before(p.leaseUntil)
}

func (p *Publisher) leaseKey() string {
	return leaseKeyPrefix + p.opts.Source
}

func (p *Publisher) maintainLease(ctx context.Context) {
	defer func() { p.leader.Store(p.holding()) }()
	begin := p.now()
	if p.holding() {
		n, err := renewScript.Run(ctx, p.rdb, []string{p.leaseKey()}, p.opts.Owner, leaseTTL.Milliseconds()).Int64()
		switch {
		case err != nil:
			// 续期失败时继续使用到本地截止时间，之后停止发布，不会和新的持有者重叠
			p.logger.Warn("renew outbox lease failed", slog.Any("error", err))
		case n == 0:
			p.logger.Error("outbox lease taken by another instance")
			p.leaseUntil = time.Time{}
		default:
			p.leaseUntil = begin.Add(leaseTTL)
			p.lastRenew = begin
		}
		return
	}
	ok, err := p.rdb.SetNX(ctx, p.leaseKey(), p.opts.Owner, leaseTTL).Result()
	if err != nil {
		p.logger.Warn("acquire outbox lease failed", slog.Any("error", err))
		return
	}
	if ok {
		p.leaseUntil = begin.Add(leaseTTL)
		p.lastRenew = begin
		p.logger.Info("outbox lease acquired")
	}
}

func (p *Publisher) releaseLease() {
	if !p.holding() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := releaseScript.Run(ctx, p.rdb, []string{p.leaseKey()}, p.opts.Owner).Err(); err != nil {
		p.logger.Warn("release outbox lease failed", slog.Any("error", err))
	}
	p.leaseUntil = time.Time{}
	p.leader.Store(false)
}

// drain 连续发布，直到没有未发布的事件、失败或租约不足。
func (p *Publisher) drain(ctx context.Context) {
	if !p.holding() || p.now().Before(p.retryAt) {
		return
	}
	for ctx.Err() == nil {
		if p.now().Sub(p.lastRenew) >= renewInterval {
			p.maintainLease(ctx)
		}
		if p.leaseUntil.Sub(p.now()) < produceTimeout {
			return
		}
		events, err := p.store.fetch(ctx, p.opts.BatchSize)
		if err != nil {
			p.fail("fetch outbox events", err)
			return
		}
		if len(events) == 0 {
			return
		}
		pctx, cancel := context.WithTimeout(ctx, produceTimeout)
		err = p.producer.produce(pctx, events)
		cancel()
		if err != nil {
			publishedCounter.Add(ctx, int64(len(events)), p.mErr)
			p.fail("produce outbox events", err)
			return
		}
		sent := p.now()
		for _, e := range events {
			publishDelay.Record(ctx, sent.Sub(e.CreatedAt).Seconds(), p.mDelay)
		}
		ids := make([]int64, len(events))
		for i, e := range events {
			ids[i] = e.ID
		}
		if err := p.store.markPublished(ctx, ids, p.now().UTC()); err != nil {
			// 已经发出但没标记，下一轮会重复发布，由消费幂等兜底
			p.fail("mark outbox events published", err)
			return
		}
		publishedCounter.Add(ctx, int64(len(events)), p.mOK)
		p.backoff = 0
		if len(events) < p.opts.BatchSize {
			return
		}
	}
}

func (p *Publisher) fail(op string, err error) {
	if p.backoff == 0 {
		p.backoff = minBackoff
	} else {
		p.backoff = min(p.backoff*2, maxBackoff)
	}
	p.retryAt = p.now().Add(p.backoff)
	p.logger.Warn(op+" failed, will retry", slog.Duration("backoff", p.backoff), slog.Any("error", err))
}

// cleanup 删除超过保留期的已发布事件。只由租约持有者执行，分批删除避免长事务。
func (p *Publisher) cleanup(ctx context.Context) {
	if !p.holding() {
		return
	}
	before := p.now().UTC().Add(-p.opts.Retention)
	var total int64
	for range cleanupMaxLoops {
		n, err := p.store.deletePublished(ctx, before, cleanupBatch)
		if err != nil {
			p.logger.Warn("clean up published outbox events failed", slog.Any("error", err))
			return
		}
		total += n
		if n < cleanupBatch {
			break
		}
	}
	if total > 0 {
		p.logger.Info("published outbox events cleaned up", slog.Int64("deleted", total))
	}
}

type gormStore struct{ db *gorm.DB }

func (s gormStore) fetch(ctx context.Context, limit int) ([]Event, error) {
	var events []Event
	// 走 idx_unpublished (published_at, id)：published_at IS NULL 的部分按 id 有序，不需要排序
	err := s.db.WithContext(ctx).Where("published_at IS NULL").Order("id").Limit(limit).Find(&events).Error
	return events, err
}

func (s gormStore) markPublished(ctx context.Context, ids []int64, at time.Time) error {
	return s.db.WithContext(ctx).Model(&Event{}).Where("id IN ?", ids).Update("published_at", at).Error
}

func (s gormStore) deletePublished(ctx context.Context, before time.Time, limit int) (int64, error) {
	res := s.db.WithContext(ctx).Exec(
		"DELETE FROM outbox_events WHERE published_at < ? ORDER BY published_at LIMIT ?", before, limit)
	return res.RowsAffected, res.Error
}

type kafkaProducer struct {
	cl     *kgo.Client
	source string
}

// produce 同步发送一批记录。franz-go 在同一分区内保持记录的提交顺序，幂等生产者保证重试不重复。
func (k kafkaProducer) produce(ctx context.Context, events []Event) error {
	recs := toRecords(k.source, events)
	spans := startPublishSpans(k.source, events, recs)
	err := k.cl.ProduceSync(ctx, recs...).FirstErr()
	for _, sp := range spans {
		if err != nil {
			sp.RecordError(err)
			sp.SetStatus(codes.Error, "produce failed")
		}
		sp.End()
	}
	if err != nil {
		return fmt.Errorf("produce %d records: %w", len(recs), err)
	}
	return nil
}

// startPublishSpans 为带 TraceParent 的事件各开一个 producer Span（publish <topic>），起点是事件写入 Outbox 的时间，
// 终点是发送完成：Trace 中能直接看到事件在 Outbox 里等了多久。Span 上下文写入记录头 traceparent，
// 消费 Span 接在它下面。没有 TraceParent 的事件（写入时不在请求链路中）不开 Span，避免产生大量单节点 Trace。
func startPublishSpans(source string, events []Event, recs []*kgo.Record) []trace.Span {
	var spans []trace.Span
	tracer := otel.Tracer(telemetry.ScopeName)
	for i, e := range events {
		if e.TraceParent == "" {
			continue
		}
		ctx, sp := tracer.Start(kafka.ContextWithTraceParent(context.Background(), e.TraceParent), "publish "+e.Topic,
			trace.WithSpanKind(trace.SpanKindProducer),
			trace.WithTimestamp(e.CreatedAt),
			trace.WithAttributes(
				attribute.String("messaging.system", "kafka"),
				attribute.String("messaging.destination.name", e.Topic),
				attribute.String("marschat.event_id", kafka.Header(recs[i], kafka.HeaderEventID)),
				attribute.String("marschat.event_type", e.EventType),
				attribute.String("marschat.outbox_source", source),
			))
		kafka.InjectTrace(ctx, recs[i])
		spans = append(spans, sp)
	}
	return spans
}

// toRecords 把事件转换成 Kafka 记录。
//
// 不设置 Record.Timestamp：franz-go 用批次中第一条记录的 Timestamp 计算投递超时
// （RecordDeliveryTimeout），填事件的创建时间会让积压的旧事件一发出就被判定超时。
// 留空时 franz-go 填发送时刻；事件发生的时间在各事件的 payload 里。
func toRecords(source string, events []Event) []*kgo.Record {
	recs := make([]*kgo.Record, len(events))
	for i, e := range events {
		recs[i] = &kgo.Record{
			Topic: e.Topic,
			Key:   []byte(e.EventKey),
			Value: e.Payload,
			Headers: []kgo.RecordHeader{
				{Key: kafka.HeaderEventType, Value: []byte(e.EventType)},
				{Key: kafka.HeaderEventID, Value: []byte(source + ":" + strconv.FormatInt(e.ID, 10))},
				{Key: kafka.HeaderSource, Value: []byte(source)},
			},
		}
	}
	return recs
}
