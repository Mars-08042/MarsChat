//go:build wireinject

// 该文件只供 wire 生成依赖注入代码（make wire），不参与正常构建。

package main

import (
	"log/slog"

	"github.com/go-kratos/kratos/v3"
	"github.com/google/wire"

	"github.com/Mars-08042/MarsChat/app/message/internal/biz"
	"github.com/Mars-08042/MarsChat/app/message/internal/conf"
	"github.com/Mars-08042/MarsChat/app/message/internal/data"
	"github.com/Mars-08042/MarsChat/app/message/internal/server"
	"github.com/Mars-08042/MarsChat/app/message/internal/service"
	"github.com/Mars-08042/MarsChat/pkg/bootstrap"
	pkgconf "github.com/Mars-08042/MarsChat/pkg/conf"
)

// *conf.Bootstrap 只用于 social、account 两个客户端：它们的配置是同一类型（Upstream），wire 无法按类型区分。
func wireApp(*pkgconf.Server, *pkgconf.Registry, *pkgconf.MySQL, *pkgconf.Redis, *pkgconf.Auth,
	*pkgconf.Kafka, *pkgconf.Outbox, *conf.Bootstrap, *biz.PolicyHolder, *slog.Logger) (*kratos.App, func(), error) {
	panic(wire.Build(server.ProviderSet, service.ProviderSet, biz.ProviderSet, data.ProviderSet,
		bootstrap.NewConsulRegistry, newApp))
}
