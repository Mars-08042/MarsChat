package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/go-kratos/kratos/contrib/registry/consul/v3"
	"github.com/go-kratos/kratos/v3"
	"github.com/go-kratos/kratos/v3/transport/grpc"

	"github.com/Mars-08042/MarsChat/app/delivery/internal/conf"
	"github.com/Mars-08042/MarsChat/pkg/bootstrap"
	"github.com/Mars-08042/MarsChat/pkg/kafka"
)

var (
	// Name 是服务名，也是在 Consul 中注册的名字。
	Name = "marschat-delivery"
	// Version 在构建时通过 -ldflags "-X main.Version=x.y.z" 注入。
	Version = "dev"

	flagconf string
)

func init() {
	flag.StringVar(&flagconf, "conf", "app/delivery/configs", "配置目录或文件路径")
}

func newApp(logger *slog.Logger, r *consul.Registry, gs *grpc.Server, consumer *kafka.Consumer) *kratos.App {
	return kratos.New(
		kratos.ID(bootstrap.InstanceID(Name)),
		kratos.Name(Name),
		kratos.Version(Version),
		kratos.Logger(logger),
		kratos.Registrar(r),
		kratos.Server(gs, consumer),
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
	c, err := bootstrap.LoadConfig(flagconf, &bc)
	if err != nil {
		return err
	}
	defer c.Close()
	logger := bootstrap.NewLogger(bc.GetLog(),
		slog.String("service.name", Name),
		slog.String("service.version", Version),
	)

	// delivery 没有可热更新的配置，修改后需要重启
	app, cleanup, err := wireApp(bc.GetServer(), bc.GetRegistry(), bc.GetRedis(), bc.GetKafka(), bc.GetPush(), &bc, logger)
	if err != nil {
		return fmt.Errorf("init app: %w", err)
	}
	defer cleanup()

	return app.Run()
}
