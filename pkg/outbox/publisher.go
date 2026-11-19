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
	"gorm.io/gorm"

	"github.com/Mars-08042/MarsChat/pkg/conf"
	"github.com/Mars-08042/MarsChat/pkg/kafka"
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
type Publisher struct {
	store    store
	producer producer
	rdb      redis.UniversalClient
	opts     Options
	logger   *slog.Logger
	now      func() time.Time

	wake chan struct{}

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
	return &Publisher{
		store:    s,
		producer: p,
		rdb:      rdb,
		opts:     opts,
		logger:   logger.With(slog.String("outbox.source", opts.Source)),
		now:      time.Now,
		wake:     make(chan struct{}, 1),
		ctx:      ctx,
		cancel:   cancel,
		done:     make(chan struct{}),
	}
}

// Notify 唤醒发布器立即检查新事件。在写了事件的事务提交后调用，不阻塞；
// 已有待处理的唤醒时直接返回，多次提交合并成一次检查。
func (p *Publisher) Notify() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// Start 持续发布直到 Stop 被调用。
func (p *Publisher) Start(context.Context) error {
	p.started.Store(true)
	defer close(p.done)
	p.logger.Info("outbox publisher started")
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
			p.fail("produce outbox events", err)
			return
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
	if err := k.cl.ProduceSync(ctx, recs...).FirstErr(); err != nil {
		return fmt.Errorf("produce %d records: %w", len(recs), err)
	}
	return nil
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
