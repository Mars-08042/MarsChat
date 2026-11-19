//go:build wireinject

// 该文件只供 wire 生成依赖注入代码（make wire），不参与正常构建。

package main

import (
	"log/slog"

	"github.com/go-kratos/kratos/v3"
	"github.com/google/wire"

	"github.com/Mars-08042/MarsChat/app/delivery/internal/biz"
	"github.com/Mars-08042/MarsChat/app/delivery/internal/conf"
	"github.com/Mars-08042/MarsChat/app/delivery/internal/data"
	"github.com/Mars-08042/MarsChat/app/delivery/internal/server"
	"github.com/Mars-08042/MarsChat/pkg/bootstrap"
	pkgconf "github.com/Mars-08042/MarsChat/pkg/conf"
)

// *conf.Bootstrap 只用于 social 客户端。
func wireApp(*pkgconf.Server, *pkgconf.Registry, *pkgconf.Redis, *pkgconf.Kafka, *conf.Push,
	*conf.Bootstrap, *slog.Logger) (*kratos.App, func(), error) {
	panic(wire.Build(server.ProviderSet, biz.ProviderSet, data.ProviderSet, bootstrap.NewConsulRegistry, newApp))
}
