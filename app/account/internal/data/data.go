// Package data 实现 biz 层定义的存储接口：MySQL 持久化、Redis 缓存与计数。
package data

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/wire"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"github.com/Mars-08042/MarsChat/app/account/internal/biz"
	"github.com/Mars-08042/MarsChat/app/account/internal/conf"
	"github.com/Mars-08042/MarsChat/pkg/auth"
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
	NewUserRepo,
	NewSessionRepo,
	NewEventRepo,
	NewCodeStore,
	NewLoginGuard,
	NewCodeSender,
	NewIDGenerator,
	NewRateLimiter,
	NewRevoker,
)

// serviceName 与 cmd 中注册到 Consul 的服务名一致，用于实例 ID、Outbox 租约和事件来源。
const serviceName = "marschat-account"

// Data 持有各 repo 共用的存储客户端。
type Data struct {
	db  *gorm.DB
	rdb *redis.Client
}

// NewData 组装存储客户端，连接的关闭由各自的 provider 负责。
func NewData(db *gorm.DB, rdb *redis.Client) *Data {
	return &Data{db: db, rdb: rdb}
}

// ReadinessChecks 返回就绪探针要检查的存储依赖。
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
// 提交成功后唤醒 Outbox 发布器：事务里可能写了事件，唤醒的开销只是一次非阻塞的 channel 发送。
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

// NewIDGenerator 创建 snowflake 生成器，节点号通过 Redis 租约分配。
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

// NewRevoker 创建吊销名单的写入端。
func NewRevoker(rdb *redis.Client) biz.Revoker {
	return auth.NewRevocations(rdb)
}

// NewCodeSender 按配置选择验证码邮件的发送方。
func NewCodeSender(c *conf.Mail, logger *slog.Logger) (biz.CodeSender, error) {
	switch c.GetProvider() {
	case "log":
		return &logCodeSender{logger: logger}, nil
	case "smtp":
		s, err := newSMTPSender(c.GetSmtp(), logger)
		if err != nil {
			return nil, err
		}
		return s, nil
	default:
		return nil, fmt.Errorf("unsupported mail provider %q", c.GetProvider())
	}
}

// logCodeSender 只把验证码写进日志，用于开发和测试，生产环境必须使用 smtp。
type logCodeSender struct {
	logger *slog.Logger
}

func (s *logCodeSender) Send(_ context.Context, email, code string, purpose int32, _ time.Duration) error {
	s.logger.Info("verification code (log provider, dev only)",
		slog.String("email", biz.MaskEmail(email)), slog.String("code", code), slog.Int("purpose", int(purpose)))
	return nil
}
