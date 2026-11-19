// Package idgen 实现 snowflake ID：41 位毫秒时间戳、10 位节点号、12 位序列号。
// 节点号通过 Redis 租约分配，保证多个实例同时发号时不会重复。
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
	node   int64
	logger *slog.Logger
	now    func() time.Time

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
			g.logger.Warn("release idgen node failed", slog.Int64("node", g.node), slog.Any("error", err))
		}
	}
	logger.Info("idgen node acquired", slog.Int64("node", g.node))
	return g, cleanup, nil
}

// acquire 从随机位置开始依次尝试节点号，减少多个实例同时启动时的冲突。
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
			g.node = node
			g.leaseUntil.Store(begin.Add(leaseTTL).UnixMilli())
			return nil
		}
	}
	return ErrNoFreeNode
}

func (g *Generator) renewLoop(stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(renewEvery)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			g.renew()
		}
	}
}

func (g *Generator) renew() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	begin := g.now()
	n, err := renewScript.Run(ctx, g.rdb, []string{g.key()}, g.owner, leaseTTL.Milliseconds()).Int64()
	switch {
	case err != nil:
		// 网络抖动时下一轮再试，租约在本地截止时间前仍然有效
		g.logger.Warn("renew idgen node lease failed", slog.Int64("node", g.node), slog.Any("error", err))
	case n == 0:
		// 键已过期并可能被其他实例占用，不能再续；本地截止时间到后发号会返回 ErrLeaseLost
		g.logger.Error("idgen node lease taken by another owner", slog.Int64("node", g.node))
	default:
		g.leaseUntil.Store(begin.Add(leaseTTL).UnixMilli())
	}
}

func (g *Generator) key() string {
	return nodeKeyPrefix + strconv.FormatInt(g.node, 10)
}

// Node 返回当前持有的节点号。
func (g *Generator) Node() int64 {
	return g.node
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
	return (nowMs-epochMs)<<(nodeBits+seqBits) | g.node<<seqBits | g.seq, nil
}

// Decompose 拆出 ID 中的毫秒时间、节点号和序列号，用于排查问题和测试。
func Decompose(id int64) (t time.Time, node, seq int64) {
	ms := id>>(nodeBits+seqBits) + epochMs
	return time.UnixMilli(ms).UTC(), id >> seqBits & maxNode, id & maxSeq
}
