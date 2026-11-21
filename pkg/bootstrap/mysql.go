package bootstrap

import (
	"context"
	"fmt"
	"log/slog"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/Mars-08042/MarsChat/pkg/conf"
)

// NewMySQL 打开 MySQL 连接池。gorm.Open 会立即 ping 一次，数据库不可用时启动失败，
// 连接超时由 DSN 中的 timeout 参数控制。
func NewMySQL(c *conf.MySQL, logger *slog.Logger) (*gorm.DB, func(), error) {
	db, err := gorm.Open(mysql.Open(c.GetDsn()), &gorm.Config{
		Logger: gormlogger.NewSlogLogger(logger.With(slog.String("component", "gorm")), gormlogger.Config{
			SlowThreshold: c.GetSlowThreshold().AsDuration(),
			// 只记录慢查询和错误；查不到记录是正常业务分支，不记错误日志
			LogLevel:                  gormlogger.Warn,
			IgnoreRecordNotFoundError: true,
			// 日志中只打印带占位符的 SQL，避免邮箱等参数落入日志
			ParameterizedQueries: true,
		}),
		// 把唯一键冲突等驱动错误转换成 gorm.ErrDuplicatedKey 等通用错误，repo 不必依赖 MySQL 错误码
		TranslateError: true,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("open mysql: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, nil, fmt.Errorf("get mysql pool: %w", err)
	}
	sqlDB.SetMaxOpenConns(int(c.GetMaxOpenConns()))
	sqlDB.SetMaxIdleConns(int(c.GetMaxIdleConns()))
	sqlDB.SetConnMaxLifetime(c.GetConnMaxLifetime().AsDuration())

	cleanup := func() {
		if err := sqlDB.Close(); err != nil {
			logger.Error("close mysql failed", slog.Any("error", err))
		}
	}
	return db, cleanup, nil
}

// MySQLCheck 返回检查 MySQL 连通性的就绪检查项。
func MySQLCheck(db *gorm.DB) Check {
	return Check{Name: "mysql", Fn: func(ctx context.Context) error {
		sqlDB, err := db.DB()
		if err != nil {
			return err
		}
		return sqlDB.PingContext(ctx)
	}}
}
