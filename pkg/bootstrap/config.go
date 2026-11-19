// Package bootstrap 提供各服务启动时共用的组件：配置加载、日志、注册中心、MySQL、Redis 和健康检查。
package bootstrap

import (
	"fmt"

	"github.com/go-kratos/kratos/v3/config"
	"github.com/go-kratos/kratos/v3/config/env"
	"github.com/go-kratos/kratos/v3/config/file"
)

// EnvPrefix 是环境变量前缀。环境变量 MARSCHAT_XXX 去掉前缀后作为配置键 XXX，
// 配置文件中的 ${XXX} 或 ${XXX:默认值} 占位符引用它。
const EnvPrefix = "MARSCHAT_"

// LoadConfig 读取配置目录（或文件）并解析到 v。
// 不支持热更新：加载完立即关闭配置源，配置变更需要重启服务。
// 占位符引用的环境变量不存在且没有默认值时会被替换成空字符串，密码类配置缺失会在连接依赖时报错。
func LoadConfig(path string, v any) error {
	c := config.New(config.WithSource(
		file.NewSource(path),
		env.NewSource(EnvPrefix),
	))
	defer c.Close()

	if err := c.Load(); err != nil {
		return fmt.Errorf("load config %s: %w", path, err)
	}
	if err := c.Scan(v); err != nil {
		return fmt.Errorf("scan config %s: %w", path, err)
	}
	return nil
}
