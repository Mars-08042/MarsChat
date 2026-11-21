// Package bootstrap 提供各服务启动时共用的组件：配置加载、日志、注册中心、MySQL、Redis 和健康检查。
package bootstrap

import (
	"fmt"
	"log/slog"

	"buf.build/go/protovalidate"
	"github.com/go-kratos/kratos/v3/config"
	"github.com/go-kratos/kratos/v3/config/env"
	"github.com/go-kratos/kratos/v3/config/file"
	"google.golang.org/protobuf/proto"
)

// EnvPrefix 是环境变量前缀。环境变量 MARSCHAT_XXX 去掉前缀后作为配置键 XXX，
// 配置文件中的 ${XXX} 或 ${XXX:默认值} 占位符引用它。
const EnvPrefix = "MARSCHAT_"

// LoadConfig 读取配置目录（或文件），解析到 v 并按 proto 中的 protovalidate 规则校验。
// 返回的 config.Config 保持打开，用于 WatchConfig 热更新，调用方在退出时 Close。
// 占位符引用的环境变量不存在且没有默认值时会被替换成空字符串，由校验规则或连接依赖时报错。
func LoadConfig(path string, v proto.Message) (config.Config, error) {
	c := config.New(config.WithSource(
		file.NewSource(path),
		env.NewSource(EnvPrefix),
	))
	if err := c.Load(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("load config %s: %w", path, err)
	}
	if err := c.Scan(v); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("scan config %s: %w", path, err)
	}
	if err := protovalidate.Validate(v); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return c, nil
}

// WatchConfig 监听配置中 key 对应的部分。配置文件变化后解析到 newT() 返回的新对象，
// 校验通过才调用 apply；解析或校验失败时保留旧值并记录错误日志。
func WatchConfig[T proto.Message](c config.Config, key string, newT func() T, apply func(T), logger *slog.Logger) error {
	return c.Watch(key, func(_ string, v config.Value) {
		t := newT()
		if err := v.Scan(t); err != nil {
			logger.Error("reload config failed, keeping previous value", slog.String("key", key), slog.Any("error", err))
			return
		}
		if err := protovalidate.Validate(t); err != nil {
			logger.Error("reloaded config is invalid, keeping previous value", slog.String("key", key), slog.Any("error", err))
			return
		}
		apply(t)
		logger.Info("config reloaded", slog.String("key", key))
	})
}
