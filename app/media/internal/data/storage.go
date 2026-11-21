package data

import (
	"fmt"
	"log/slog"

	"github.com/Mars-08042/MarsChat/app/media/internal/biz"
	"github.com/Mars-08042/MarsChat/app/media/internal/conf"
)

// NewStorage 按配置选择文件存储。配置不完整时启动失败；OSS 只校验配置，不访问网络：
// 网络暂时不通不应阻止服务启动，上传时再返回 503。
func NewStorage(c *conf.Storage, logger *slog.Logger) (biz.Storage, error) {
	switch c.GetProvider() {
	case "local":
		s, err := newLocalStorage(c.GetLocal())
		if err != nil {
			return nil, err
		}
		logger.Info("file storage: local", slog.String("dir", s.dir), slog.String("public_base_url", s.baseURL))
		return s, nil
	case "oss":
		s, err := newOSSStorage(c.GetOss())
		if err != nil {
			return nil, err
		}
		// 不打印 AccessKey
		logger.Info("file storage: oss", slog.String("region", c.GetOss().GetRegion()),
			slog.String("bucket", s.bucket), slog.String("key_prefix", s.prefix))
		return s, nil
	default:
		return nil, fmt.Errorf("unsupported storage provider %q", c.GetProvider())
	}
}
