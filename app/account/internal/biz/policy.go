package biz

import (
	"sync/atomic"
	"time"

	"github.com/Mars-08042/MarsChat/app/account/internal/conf"
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
