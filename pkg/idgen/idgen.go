// Package idgen 实现 snowflake ID：41 位毫秒时间戳、10 位节点号、12 位序列号。
// 节点号通过 Redis 租约分配，保证多个实例同时发号时不会重复。
// 租约丢失（例如 Redis 停机超过 TTL，重启后键已过期）时换一个空闲节点号继续发号，不需要重启服务。
package idgen

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// epochMs 是时间戳起点 2026-01-01T00:00:00Z，41 位毫秒可以用到约 2095 年
	epochMs  = 1767225600000
	nodeBits = 10
	seqBits  = 12
	maxNode  = 1<<nodeBits - 1
	maxSeq   = 1<<seqBits - 1

	leaseTTL   = 30 * time.Second
	renewEvery = 10 * time.Second
	// 租约已失效时的重试间隔：Redis 恢复后尽快重新拿到节点号，减少写操作失败的时间
	retryEvery = time.Second
	// 时钟回拨不超过该值时等待追平，超过则报错
	maxBackwardWait = 5 * time.Millisecond

	nodeKeyPrefix = "idgen:node:"
)

var (
	// ErrLeaseLost 表示节点号租约已过期（例如长时间连不上 Redis），继续发号可能与接手该节点号的实例重复。
	ErrLeaseLost = errors.New("idgen: node lease lost")
	// ErrClockBackwards 表示系统时钟回拨超过容忍范围。
	ErrClockBackwards = errors.New("idgen: clock moved backwards")
	// ErrNoFreeNode 表示 1024 个节点号都被占用。
	ErrNoFreeNode = errors.New("idgen: no free node id")
)

// 只有仍是持有者时才续期或释放，避免误操作其他实例接手的节点号
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

// Generator 是并发安全的 ID 生成器。
type Generator struct {
	rdb    redis.UniversalClient
	owner  string
	logger *slog.Logger
	now    func() time.Time

	// node 是当前持有的节点号，租约丢失后会换成新的；切换时持有 mu，NextID 不会读到新旧混合的状态
	node atomic.Int64
	// leaseUntil 是本地认定租约有效的截止时间（Unix 毫秒）。取续期请求发出前的时间加上 TTL，
	// 一定早于 Redis 中键的实际过期时间，所以本地停止发号总是先于别的实例拿到该节点号。
	leaseUntil atomic.Int64

	mu     sync.Mutex
	lastMs int64
	seq    int64
}

// New 抢占一个空闲节点号并启动后台续期。owner 用于标识持有者，应在实例间唯一（例如实例 ID）。
// 返回的 cleanup 停止续期并释放节点号。
func New(ctx context.Context, rdb redis.UniversalClient, owner string, logger *slog.Logger) (*Generator, func(), error) {
	g := &Generator{rdb: rdb, owner: owner, logger: logger, now: time.Now}
	if err := g.acquire(ctx); err != nil {
		return nil, nil, err
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	go g.renewLoop(stop, done)
	cleanup := func() {
		close(stop)
		<-done
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := releaseScript.Run(ctx, g.rdb, []string{g.key()}, g.owner).Err(); err != nil {
			g.logger.Warn("release idgen node failed", slog.Int64("node", g.Node()), slog.Any("error", err))
		}
	}
	logger.Info("idgen node acquired", slog.Int64("node", g.Node()))
	return g, cleanup, nil
}

// acquire 从随机位置开始依次尝试节点号，减少多个实例同时启动时的冲突。
// 拿到后在 mu 保护下同时切换节点号和截止时间。
func (g *Generator) acquire(ctx context.Context) error {
	start := rand.IntN(maxNode + 1)
	for i := 0; i <= maxNode; i++ {
		node := int64((start + i) % (maxNode + 1))
		begin := g.now()
		ok, err := g.rdb.SetNX(ctx, nodeKeyPrefix+strconv.FormatInt(node, 10), g.owner, leaseTTL).Result()
		if err != nil {
			return fmt.Errorf("acquire idgen node: %w", err)
		}
		if ok {
			g.mu.Lock()
			g.node.Store(node)
			g.leaseUntil.Store(begin.Add(leaseTTL).UnixMilli())
			g.mu.Unlock()
			return nil
		}
	}
	return ErrNoFreeNode
}

// renewLoop 定期续期。续期失败或租约失效期间改为每秒重试：Redis 短暂不可用后能在本地截止时间前续上，
// 而不是等满 renewEvery——那样下一轮续期可能恰好晚于截止时间，中间出现发号失败的窗口（TD-39）。
func (g *Generator) renewLoop(stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	timer := time.NewTimer(renewEvery)
	defer timer.Stop()
	for {
		select {
		case <-stop:
			return
		case <-timer.C:
			timer.Reset(g.nextRenewDelay(g.renew()))
		}
	}
}

// nextRenewDelay 返回下一次续期的间隔：本次续上且租约有效时按 renewEvery，否则按 retryEvery 尽快重试。
func (g *Generator) nextRenewDelay(renewed bool) time.Duration {
	if renewed && g.holding() {
		return renewEvery
	}
	return retryEvery
}

func (g *Generator) holding() bool {
	return g.now().UnixMilli() < g.leaseUntil.Load()
}

// renew 续期当前节点号，键已失效时换一个空闲节点号。返回是否持有有效租约（续上或换到了新节点号）。
func (g *Generator) renew() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	begin := g.now()
	n, err := renewScript.Run(ctx, g.rdb, []string{g.key()}, g.owner, leaseTTL.Milliseconds()).Int64()
	switch {
	case err != nil:
		// 网络抖动时尽快重试，租约在本地截止时间前仍然有效；过了截止时间发号返回 ErrLeaseLost
		g.logger.Warn("renew idgen node lease failed", slog.Int64("node", g.Node()), slog.Any("error", err))
		return false
	case n == 0:
		// 键已过期（Redis 停机超过 TTL）或被其他实例占用，不能再续这个节点号。
		// 立即停止用它发号，再换一个空闲节点号：新节点号上的 ID 不会与旧节点号的 ID 冲突，不必等旧租约到期
		old := g.Node()
		g.mu.Lock()
		g.leaseUntil.Store(0)
		g.mu.Unlock()
		if err := g.acquire(ctx); err != nil {
			g.logger.Error("idgen node lease lost, reacquire failed", slog.Int64("node", old), slog.Any("error", err))
			return false
		}
		g.logger.Warn("idgen node lease lost, switched to a new node", slog.Int64("old_node", old), slog.Int64("node", g.Node()))
		return true
	default:
		g.leaseUntil.Store(begin.Add(leaseTTL).UnixMilli())
		return true
	}
}

func (g *Generator) key() string {
	return nodeKeyPrefix + strconv.FormatInt(g.Node(), 10)
}

// Node 返回当前持有的节点号。租约丢失后会换成新的节点号。
func (g *Generator) Node() int64 {
	return g.node.Load()
}

// NextID 生成下一个 ID。
func (g *Generator) NextID() (int64, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	nowMs := g.now().UnixMilli()
	if nowMs >= g.leaseUntil.Load() {
		return 0, ErrLeaseLost
	}
	if nowMs < g.lastMs {
		backward := time.Duration(g.lastMs-nowMs) * time.Millisecond
		if backward > maxBackwardWait {
			return 0, fmt.Errorf("%w by %s", ErrClockBackwards, backward)
		}
		time.Sleep(backward)
		nowMs = g.now().UnixMilli()
		if nowMs < g.lastMs {
			return 0, ErrClockBackwards
		}
	}
	if nowMs == g.lastMs {
		g.seq = (g.seq + 1) & maxSeq
		if g.seq == 0 {
			// 同一毫秒的序列号用完，等到下一毫秒
			for nowMs <= g.lastMs {
				time.Sleep(100 * time.Microsecond)
				nowMs = g.now().UnixMilli()
			}
		}
	} else {
		g.seq = 0
	}
	g.lastMs = nowMs
	return (nowMs-epochMs)<<(nodeBits+seqBits) | g.node.Load()<<seqBits | g.seq, nil
}

// Decompose 拆出 ID 中的毫秒时间、节点号和序列号，用于排查问题和测试。
func Decompose(id int64) (t time.Time, node, seq int64) {
	ms := id>>(nodeBits+seqBits) + epochMs
	return time.UnixMilli(ms).UTC(), id >> seqBits & maxNode, id & maxSeq
}
