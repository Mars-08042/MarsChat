// Package data 实现 biz 层定义的存储接口：MySQL 持久化、Redis 缓存与计数，以及对 account 的调用。
package data

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/wire"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"github.com/Mars-08042/MarsChat/app/social/internal/biz"
	"github.com/Mars-08042/MarsChat/pkg/bootstrap"
	"github.com/Mars-08042/MarsChat/pkg/idgen"
	"github.com/Mars-08042/MarsChat/pkg/ratelimit"
)

// ProviderSet 是 data 层的依赖提供者。
var ProviderSet = wire.NewSet(
	bootstrap.NewMySQL,
	bootstrap.NewRedis,
	NewData,
	NewTransaction,
	NewFriendRepo,
	NewFriendRequestRepo,
	NewBlockRepo,
	NewGroupRepo,
	NewMemberRepo,
	NewJoinRequestRepo,
	NewEventRepo,
	NewAccountClient,
	NewUserService,
	NewIDGenerator,
	NewRateLimiter,
)

// Data 持有各 repo 共用的存储客户端。
type Data struct {
	db  *gorm.DB
	rdb *redis.Client
}

// NewData 组装存储客户端，连接的关闭由各自的 provider 负责。
func NewData(db *gorm.DB, rdb *redis.Client) *Data {
	return &Data{db: db, rdb: rdb}
}

// ReadinessChecks 返回就绪探针要检查的存储依赖。account 不在其中：
// 它不可用时 social 降级运行（列表不带资料、申请好友返回 503），不应被摘掉流量。
func (d *Data) ReadinessChecks() []bootstrap.Check {
	return []bootstrap.Check{bootstrap.MySQLCheck(d.db), bootstrap.RedisCheck(d.rdb)}
}

type txKey struct{}

// DB 返回 ctx 中的事务；不在事务中时返回普通连接。
func (d *Data) DB(ctx context.Context) *gorm.DB {
	if tx, ok := ctx.Value(txKey{}).(*gorm.DB); ok {
		return tx
	}
	return d.db.WithContext(ctx)
}

func inTx(ctx context.Context) bool {
	_, ok := ctx.Value(txKey{}).(*gorm.DB)
	return ok
}

type transaction struct{ d *Data }

// NewTransaction 创建事务管理器。
func NewTransaction(d *Data) biz.Transaction {
	return &transaction{d: d}
}

// InTx 开启事务执行 fn。已经在事务中时直接复用，不开嵌套事务。
func (t *transaction) InTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if inTx(ctx) {
		return fn(ctx)
	}
	return t.d.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(context.WithValue(ctx, txKey{}, tx))
	})
}

// NewIDGenerator 创建 snowflake 生成器，节点号通过 Redis 租约分配，与其他服务共用节点号空间。
func NewIDGenerator(rdb *redis.Client, logger *slog.Logger) (biz.IDGenerator, func(), error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	g, cleanup, err := idgen.New(ctx, rdb, bootstrap.InstanceID("marschat-social"), logger)
	if err != nil {
		return nil, nil, fmt.Errorf("init id generator: %w", err)
	}
	return g, cleanup, nil
}

// NewRateLimiter 创建滑动窗口限流器。
func NewRateLimiter(rdb *redis.Client) biz.RateLimiter {
	return ratelimit.New(rdb)
}
