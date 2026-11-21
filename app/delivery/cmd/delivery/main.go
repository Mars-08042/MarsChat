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

	"github.com/Mars-08042/MarsChat/app/delivery/internal/biz"
	"github.com/Mars-08042/MarsChat/app/delivery/internal/conf"
	"github.com/Mars-08042/MarsChat/pkg/bootstrap"
	"github.com/Mars-08042/MarsChat/pkg/kafka"
	"github.com/Mars-08042/MarsChat/pkg/telemetry"
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

func newApp(logger *slog.Logger, r *consul.Registry, hs *http.Server, gs *grpc.Server, consumer *kafka.Consumer,
	lag *kafka.LagMonitor) *kratos.App {
	return kratos.New(
		kratos.ID(bootstrap.InstanceID(Name)),
		kratos.Name(Name),
		kratos.Version(Version),
		kratos.Logger(logger),
		kratos.Registrar(r),
		// HTTP 服务器放在第一个：Kratos 把第一个端点登记为 Consul 服务的 Address:Port，Prometheus 按它抓取 /metrics
		kratos.Server(hs, gs, consumer, lag),
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
	shutdownTelemetry, err := telemetry.Setup(bc.GetTelemetry(),
		telemetry.Info{Name: Name, Version: Version, InstanceID: bootstrap.InstanceID(Name)}, logger)
	if err != nil {
		return err
	}
	// 在 cleanup 之后执行，停机期间产生的 Span 也能导出
	defer shutdownTelemetry()

	// 只有 policy 段热更新，其余配置修改后需要重启
	policy := biz.NewPolicyHolder(bc.GetPolicy())
	if err := bootstrap.WatchConfig(c, "policy", func() *conf.Policy { return &conf.Policy{} }, policy.Store, logger); err != nil {
		return fmt.Errorf("watch policy config: %w", err)
	}

	app, cleanup, err := wireApp(bc.GetServer(), bc.GetRegistry(), bc.GetRedis(), bc.GetKafka(), bc.GetPush(), &bc, policy, logger)
	if err != nil {
		return fmt.Errorf("init app: %w", err)
	}
	defer cleanup()

	return app.Run()
}
