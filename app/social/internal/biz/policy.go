package biz

import (
	"context"
	"log/slog"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/Mars-08042/MarsChat/app/social/internal/conf"
	pkgconf "github.com/Mars-08042/MarsChat/pkg/conf"
)

// nowUTC 是各用例的时钟。截断到毫秒与数据库的 DATETIME(3) 一致：MySQL 对多余的小数位四舍五入，
// 不截断的话写入后读回的时间与内存中的不相等，返回给客户端的时间也会前后不一致。
func nowUTC() time.Time {
	return time.Now().UTC().Truncate(time.Millisecond)
}

// PolicyHolder 持有当前生效的限额策略。配置热更新时整体替换，读取方每次请求取一次快照，
// 同一个请求内看到的策略是一致的。
type PolicyHolder struct {
	v atomic.Pointer[conf.Policy]
}

// NewPolicyHolder 用启动时的配置初始化。
func NewPolicyHolder(p *conf.Policy) *PolicyHolder {
	h := &PolicyHolder{}
	h.v.Store(p)
	return h
}

// Load 返回当前策略，调用方不能修改返回值。
func (h *PolicyHolder) Load() *conf.Policy {
	return h.v.Load()
}

// Store 替换策略，由配置热更新调用（新值已通过校验）。
func (h *PolicyHolder) Store(p *conf.Policy) {
	h.v.Store(p)
}

// checkRate 按用户限流。Redis 故障时放行：这些限额用于防骚扰，不值得为它让功能整体不可用。
func checkRate(ctx context.Context, limiter RateLimiter, logger *slog.Logger, name string, userID int64, l *pkgconf.RateLimit) error {
	key := "social:rl:" + name + ":" + strconv.FormatInt(userID, 10)
	res, err := limiter.Allow(ctx, key, int(l.GetLimit()), l.GetWindow().AsDuration())
	if err != nil {
		logger.Warn("rate limiter unavailable", slog.String("limit", name), slog.Any("error", err))
		return nil
	}
	if !res.Allowed {
		return ErrRateLimited(res.RetryAfter)
	}
	return nil
}

const (
	defaultPageSize = 20
	maxPageSize     = 100
)

// pageLimit 把请求中的 limit 规范到 [1, maxPageSize]，0 取默认值。
func pageLimit(n int32) int {
	switch {
	case n <= 0:
		return defaultPageSize
	case n > maxPageSize:
		return maxPageSize
	default:
		return int(n)
	}
}

// paginate 处理多查一条的结果：items 超过 limit 条说明还有下一页，
// 截断到 limit 条并返回最后一条的游标；否则返回 0。
func paginate[T any](items []T, limit int, cursor func(T) int64) ([]T, int64) {
	if len(items) <= limit {
		return items, 0
	}
	items = items[:limit]
	return items, cursor(items[limit-1])
}

// ProfileLoader 为列表补全用户资料。account 不可用时返回空结果并记 WARN，
// 列表仍然可用，只是不带资料（设计 3.8）。
type ProfileLoader struct {
	users  UserService
	logger *slog.Logger
}

// NewProfileLoader 创建资料补全器。
func NewProfileLoader(users UserService, logger *slog.Logger) *ProfileLoader {
	return &ProfileLoader{users: users, logger: logger}
}

// Load 返回 ids 对应的资料，不存在或获取失败的 ID 不在结果中。
func (l *ProfileLoader) Load(ctx context.Context, ids []int64) map[int64]*UserProfile {
	if len(ids) == 0 {
		return map[int64]*UserProfile{}
	}
	m, err := l.users.BatchGetUsers(ctx, uniqueIDs(ids))
	if err != nil {
		l.logger.Warn("load user profiles failed, returning list without profiles",
			slog.Int("users", len(ids)), slog.Any("error", err))
		return map[int64]*UserProfile{}
	}
	return m
}

func uniqueIDs(ids []int64) []int64 {
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
