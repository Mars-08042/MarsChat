//go:build wireinject

// 该文件只供 wire 生成依赖注入代码（make wire），不参与正常构建。

package main

import (
	"log/slog"

	"github.com/go-kratos/kratos/v3"
	"github.com/google/wire"

	"github.com/Mars-08042/MarsChat/app/access/internal/biz"
	"github.com/Mars-08042/MarsChat/app/access/internal/conf"
	"github.com/Mars-08042/MarsChat/app/access/internal/data"
	"github.com/Mars-08042/MarsChat/app/access/internal/server"
	"github.com/Mars-08042/MarsChat/app/access/internal/service"
	"github.com/Mars-08042/MarsChat/pkg/bootstrap"
	pkgconf "github.com/Mars-08042/MarsChat/pkg/conf"
)

// *conf.Bootstrap 用于 message、social 客户端和 advertise_addr 这类只有 access 才有的字段。
func wireApp(*pkgconf.Server, *pkgconf.Registry, *pkgconf.Redis, *pkgconf.Auth,
	*conf.WebSocket, *conf.Call, *conf.Bootstrap, *biz.PolicyHolder, *slog.Logger) (*kratos.App, func(), error) {
	panic(wire.Build(server.ProviderSet, service.ProviderSet, biz.ProviderSet, data.ProviderSet,
		bootstrap.NewConsulRegistry, newApp))
}
