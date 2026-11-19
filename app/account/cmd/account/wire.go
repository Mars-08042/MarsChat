//go:build wireinject

// 该文件只供 wire 生成依赖注入代码（make wire），不参与正常构建。

package main

import (
	"log/slog"

	"github.com/go-kratos/kratos/v3"
	"github.com/google/wire"

	"github.com/Mars-08042/MarsChat/app/account/internal/biz"
	"github.com/Mars-08042/MarsChat/app/account/internal/conf"
	"github.com/Mars-08042/MarsChat/app/account/internal/data"
	"github.com/Mars-08042/MarsChat/app/account/internal/server"
	"github.com/Mars-08042/MarsChat/app/account/internal/service"
	"github.com/Mars-08042/MarsChat/pkg/bootstrap"
	pkgconf "github.com/Mars-08042/MarsChat/pkg/conf"
)

func wireApp(*pkgconf.Server, *pkgconf.Registry, *pkgconf.MySQL, *pkgconf.Redis, *pkgconf.Auth,
	*conf.Token, *conf.Sms, *conf.Security, *conf.Cache, *biz.PolicyHolder, *slog.Logger) (*kratos.App, func(), error) {
	panic(wire.Build(server.ProviderSet, service.ProviderSet, biz.ProviderSet, data.ProviderSet,
		bootstrap.NewConsulRegistry, newApp))
}
