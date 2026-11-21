package kafka

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/Mars-08042/MarsChat/pkg/conf"
	"github.com/Mars-08042/MarsChat/pkg/telemetry"
)

// lagQueryTimeout 是一次积压查询（列出消费组、取提交位点和分区末尾位点）的超时
const lagQueryTimeout = 10 * time.Second

// LagMonitor 定期查询名字带指定前缀的消费组在各 topic 上的积压（未消费的记录数），
// 由指标 marschat_kafka_consumer_lag{group, topic} 输出。实现 Kratos 的 transport.Server，随服务启停。
//
// 查询放在后台按固定间隔执行，Prometheus 抓取时只读缓存：Kafka 不可用时抓取不会被拖慢。
// 查询失败时清空缓存，指标随之消失，不输出过期的值；连续失败只在开始和恢复时各记一条日志。
// 多个实例部署时每个实例报告的值相同，面板和告警按 max 聚合。
type LagMonitor struct {
	cl       *kgo.Client
	adm      *kadm.Client
	prefix   string
	interval time.Duration
	logger   *slog.Logger
	reg      metric.Registration

	mu      sync.Mutex
	lags    map[groupTopic]int64
	failing bool

	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	closeOnce sync.Once
}

type groupTopic struct{ group, topic string }

// NewLagMonitor 创建积压监控。prefix 是要统计的消费组名前缀，例如 marschat-。
func NewLagMonitor(c *conf.Kafka, prefix string, interval time.Duration, logger *slog.Logger) (*LagMonitor, func(), error) {
	brokers := Brokers(c)
	if len(brokers) == 0 {
		return nil, nil, fmt.Errorf("kafka brokers not configured")
	}
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers...), kgo.ClientID("marschat-lag-monitor"))
	if err != nil {
		return nil, nil, fmt.Errorf("create kafka admin client: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &LagMonitor{
		cl:       cl,
		adm:      kadm.NewClient(cl),
		prefix:   prefix,
		interval: interval,
		logger:   logger.With(slog.String("component", "kafka-lag")),
		lags:     map[groupTopic]int64{},
		ctx:      ctx,
		cancel:   cancel,
		done:     make(chan struct{}),
	}
	meter := otel.Meter(telemetry.ScopeName)
	gauge, err := meter.Int64ObservableGauge("marschat_kafka_consumer_lag",
		metric.WithDescription("消费组在 topic 上未消费的记录数"))
	if err != nil {
		cl.Close()
		return nil, nil, fmt.Errorf("create lag gauge: %w", err)
	}
	m.reg, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		m.mu.Lock()
		defer m.mu.Unlock()
		for k, v := range m.lags {
			o.ObserveInt64(gauge, v, metric.WithAttributes(attribute.String("group", k.group), attribute.String("topic", k.topic)))
		}
		return nil
	}, gauge)
	if err != nil {
		cl.Close()
		return nil, nil, fmt.Errorf("register lag callback: %w", err)
	}
	return m, m.close, nil
}

// Start 立即查询一次，之后每隔 interval 查询，直到 Stop。
func (m *LagMonitor) Start(context.Context) error {
	defer close(m.done)
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		m.refresh(m.ctx)
		select {
		case <-m.ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Stop 停止查询并释放客户端。
func (m *LagMonitor) Stop(ctx context.Context) error {
	m.cancel()
	select {
	case <-m.done:
	case <-ctx.Done():
	}
	m.close()
	return nil
}

// close 可以重复调用：Stop 之后 wire 的 cleanup 还会再调一次。
func (m *LagMonitor) close() {
	m.closeOnce.Do(func() {
		m.cancel()
		_ = m.reg.Unregister()
		m.cl.Close()
	})
}

func (m *LagMonitor) refresh(ctx context.Context) {
	qctx, cancel := context.WithTimeout(ctx, lagQueryTimeout)
	defer cancel()
	lags, err := m.query(qctx)
	if ctx.Err() != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		m.lags = map[groupTopic]int64{}
		if !m.failing {
			m.logger.Warn("query consumer lag failed, lag metric cleared until it recovers", slog.Any("error", err))
		}
		m.failing = true
		return
	}
	if m.failing {
		m.logger.Info("query consumer lag recovered")
	}
	m.failing = false
	m.lags = lags
}

func (m *LagMonitor) query(ctx context.Context) (map[groupTopic]int64, error) {
	listed, err := m.adm.ListGroups(ctx)
	if err != nil {
		return nil, fmt.Errorf("list groups: %w", err)
	}
	var groups []string
	for _, g := range listed.Groups() {
		if strings.HasPrefix(g, m.prefix) {
			groups = append(groups, g)
		}
	}
	if len(groups) == 0 {
		return map[groupTopic]int64{}, nil
	}
	described, err := m.adm.Lag(ctx, groups...)
	if err != nil {
		return nil, fmt.Errorf("describe lag: %w", err)
	}
	return sumLags(described), nil
}

// sumLags 按消费组和 topic 汇总各分区的积压。查询出错的消费组和分区（Lag 为 -1）跳过，不当作 0：
// 少一条序列比报一个错误的 0 更不容易误导。
func sumLags(described kadm.DescribedGroupLags) map[groupTopic]int64 {
	out := map[groupTopic]int64{}
	for _, g := range described {
		if g.Error() != nil {
			continue
		}
		for topic, parts := range g.Lag {
			var total int64
			valid := false
			for _, p := range parts {
				if p.Err != nil || p.Lag < 0 {
					continue
				}
				total += p.Lag
				valid = true
			}
			if valid {
				out[groupTopic{group: g.Group, topic: topic}] = total
			}
		}
	}
	return out
}
