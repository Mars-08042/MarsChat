package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/go-kratos/kratos/contrib/registry/consul/v3"
	"github.com/go-kratos/kratos/v3"
	"github.com/go-kratos/kratos/v3/transport/grpc"
	"github.com/go-kratos/kratos/v3/transport/http"

	"github.com/Mars-08042/MarsChat/app/social/internal/conf"
	"github.com/Mars-08042/MarsChat/pkg/bootstrap"
)

var (
	// Name 是服务名，也是在 Consul 中注册的名字。
	Name = "marschat-social"
	// Version 在构建时通过 -ldflags "-X main.Version=x.y.z" 注入。
	Version = "dev"

	flagconf string
)

func init() {
	flag.StringVar(&flagconf, "conf", "app/social/configs", "配置目录或文件路径")
}

func newApp(logger *slog.Logger, r *consul.Registry, gs *grpc.Server, hs *http.Server) *kratos.App {
	return kratos.New(
		kratos.ID(bootstrap.InstanceID(Name)),
		kratos.Name(Name),
		kratos.Version(Version),
		kratos.Logger(logger),
		kratos.Registrar(r),
		kratos.Server(gs, hs),
	)
}

func main() {
	flag.Parse()
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", Name, err)
		os.Exit(1)
	}
}

func run() error {
	var bc conf.Bootstrap
	if err := bootstrap.LoadConfig(flagconf, &bc); err != nil {
		return err
	}
	logger := bootstrap.NewLogger(bc.GetLog(),
		slog.String("service.name", Name),
		slog.String("service.version", Version),
	)

	app, cleanup, err := wireApp(bc.GetServer(), bc.GetRegistry(), bc.GetMysql(), bc.GetRedis(), logger)
	if err != nil {
		return fmt.Errorf("init app: %w", err)
	}
	defer cleanup()

	return app.Run()
}
