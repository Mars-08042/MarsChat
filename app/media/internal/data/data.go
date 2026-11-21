// Package data 实现 biz 层定义的存储接口：MySQL 文件记录、Redis 缓存与限流，以及 local、oss 两种文件存储。
package data

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/wire"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"github.com/Mars-08042/MarsChat/app/media/internal/biz"
	"github.com/Mars-08042/MarsChat/pkg/bootstrap"
	"github.com/Mars-08042/MarsChat/pkg/idgen"
	"github.com/Mars-08042/MarsChat/pkg/ratelimit"
)

// ProviderSet 是 data 层的依赖提供者。
var ProviderSet = wire.NewSet(
	bootstrap.NewMySQL,
	bootstrap.NewRedis,
	NewData,
	NewFileRepo,
	NewStorage,
	NewIDGenerator,
	NewRateLimiter,
)

// serviceName 与 cmd 中注册到 Consul 的服务名一致，用于实例 ID。
const serviceName = "marschat-media"

// Data 持有各 repo 共用的存储客户端。
type Data struct {
	db  *gorm.DB
	rdb *redis.Client
}

// NewData 组装存储客户端，连接的关闭由各自的 provider 负责。
func NewData(db *gorm.DB, rdb *redis.Client) *Data {
	return &Data{db: db, rdb: rdb}
}

// ReadinessChecks 返回就绪探针要检查的存储依赖。文件存储不在其中：OSS 暂时不可达时，
// 下载地址照常签发（本地计算），不应让实例从负载均衡中摘除。
func (d *Data) ReadinessChecks() []bootstrap.Check {
	return []bootstrap.Check{bootstrap.MySQLCheck(d.db), bootstrap.RedisCheck(d.rdb)}
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
