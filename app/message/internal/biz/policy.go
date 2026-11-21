package biz

import (
	"strconv"
	"sync/atomic"
	"time"

	"github.com/Mars-08042/MarsChat/app/message/internal/conf"
)

// nowUTC 是各用例的时钟。截断到毫秒与数据库的 DATETIME(3) 一致，写入后读回的时间与内存中相等。
func nowUTC() time.Time {
	return time.Now().UTC().Truncate(time.Millisecond)
}

// PolicyHolder 持有当前生效的限额策略。配置热更新时整体替换，读取方每次请求取一次快照。
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

const (
	defaultPageSize       = 20
	maxPageSize           = 100
	defaultDifferenceSize = 100
)

// pageLimit 把请求中的 limit 规范到 [1, max]，0 取默认值。
func pageLimit(n int32, def, max int) int {
	switch {
	case n <= 0:
		return min(def, max)
	case int(n) > max:
		return max
	default:
		return int(n)
	}
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

func idKey(id int64) string {
	return strconv.FormatInt(id, 10)
}
