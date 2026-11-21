// Package data 实现 biz 层定义的存储接口：MySQL 持久化、Redis 限流，以及对 social、account 的调用。
package data

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/wire"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"github.com/Mars-08042/MarsChat/app/message/internal/biz"
	"github.com/Mars-08042/MarsChat/pkg/bootstrap"
	pkgconf "github.com/Mars-08042/MarsChat/pkg/conf"
	"github.com/Mars-08042/MarsChat/pkg/idgen"
	"github.com/Mars-08042/MarsChat/pkg/outbox"
	"github.com/Mars-08042/MarsChat/pkg/ratelimit"
)

// ProviderSet 是 data 层的依赖提供者。
var ProviderSet = wire.NewSet(
	bootstrap.NewMySQL,
	bootstrap.NewRedis,
	NewData,
	NewTransaction,
	NewPublisher,
	NewConversationRepo,
	NewMemberRepo,
	NewMessageRepo,
	NewEventRepo,
	NewSocialClient,
	NewPermissionChecker,
	NewGroupService,
	NewAccountClient,
	NewUserService,
	NewMediaClient,
	NewFileService,
	NewIDGenerator,
	NewRateLimiter,
)

// serviceName 与 cmd 中注册到 Consul 的服务名一致，用于实例 ID、Outbox 租约和事件来源。
const serviceName = "marschat-message"

// Data 持有各 repo 共用的存储客户端。
type Data struct {
	db  *gorm.DB
	rdb *redis.Client
}

// NewData 组装存储客户端，连接的关闭由各自的 provider 负责。
func NewData(db *gorm.DB, rdb *redis.Client) *Data {
	return &Data{db: db, rdb: rdb}
}

// ReadinessChecks 返回就绪探针要检查的存储依赖。social、account、Kafka 不在其中：
// 它们不可用时 message 降级运行（发消息返回 503、列表不带资料、事件积压在 Outbox），不应被摘掉流量。
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

type transaction struct {
	d   *Data
	pub *outbox.Publisher
}

// NewTransaction 创建事务管理器。
func NewTransaction(d *Data, pub *outbox.Publisher) biz.Transaction {
	return &transaction{d: d, pub: pub}
}

// InTx 开启事务执行 fn。已经在事务中时直接复用，不开嵌套事务。
// 提交成功后唤醒 Outbox 发布器，新消息的事件不必等轮询。
func (t *transaction) InTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if inTx(ctx) {
		return fn(ctx)
	}
	err := t.d.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(context.WithValue(ctx, txKey{}, tx))
	})
	if err == nil {
		t.pub.Notify()
	}
	return err
}

// NewPublisher 创建 Outbox 发布器，作为 Kratos server 随服务启停。
func NewPublisher(db *gorm.DB, rdb *redis.Client, kc *pkgconf.Kafka, oc *pkgconf.Outbox, logger *slog.Logger) (*outbox.Publisher, func(), error) {
	return outbox.New(db, rdb, kc, oc, serviceName, bootstrap.InstanceID(serviceName), logger)
}

// NewIDGenerator 创建 snowflake 生成器（单聊会话 ID），节点号与其他服务共用，ID 不会与群 ID 重复。
func NewIDGenerator(rdb *redis.Client, logger *slog.Logger) (biz.IDGenerator, func(), error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	g, cleanup, err := idgen.New(ctx, rdb, bootstrap.InstanceID(serviceName), logger)
	if err != nil {
		return nil, nil, fmt.Errorf("init id generator: %w", err)
	}
	return g, cleanup, nil
}

// NewRateLimiter 创建滑动窗口限流器。
func NewRateLimiter(rdb *redis.Client) biz.RateLimiter {
	return ratelimit.New(rdb)
}
