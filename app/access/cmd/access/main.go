package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/go-kratos/kratos/contrib/registry/consul/v3"
	"github.com/go-kratos/kratos/v3"
	"github.com/go-kratos/kratos/v3/transport/grpc"
	"github.com/go-kratos/kratos/v3/transport/http"

	"github.com/Mars-08042/MarsChat/app/access/internal/biz"
	"github.com/Mars-08042/MarsChat/app/access/internal/conf"
	"github.com/Mars-08042/MarsChat/app/access/internal/data"
	"github.com/Mars-08042/MarsChat/app/access/internal/server"
	"github.com/Mars-08042/MarsChat/pkg/bootstrap"
	"github.com/Mars-08042/MarsChat/pkg/telemetry"
)

var (
	// Name 是服务名，也是在 Consul 中注册的名字。
	Name = "marschat-access"
	// Version 在构建时通过 -ldflags "-X main.Version=x.y.z" 注入。
	Version = "dev"

	flagconf string
)

// stopTimeout 是停机时每个服务器的最长等待时间，覆盖 websocket.drain_window 加上关闭握手的宽限。
// Compose 中 access 的 stop_grace_period 比它多留 5 秒。
const stopTimeout = 15 * time.Second

func init() {
	flag.StringVar(&flagconf, "conf", "app/access/configs", "配置目录或文件路径")
}

func newApp(logger *slog.Logger, r *consul.Registry, gs *grpc.Server, hs *http.Server, hub *biz.Hub,
	routes *data.RouteRepo) *kratos.App {
	return kratos.New(
		kratos.ID(bootstrap.InstanceID(Name)),
		kratos.Name(Name),
		kratos.Version(Version),
		kratos.Logger(logger),
		kratos.Registrar(r),
		// HTTP 服务器放在第一个：Kratos 把第一个端点登记为 Consul 服务的 Address:Port，APISIX 按它转发 HTTP；
		// gRPC 客户端按协议从 TaggedAddresses 取端点，不受顺序影响。
		// 停机时 Kratos 先从 Consul 注销，再并发停止各服务器；gRPC 服务器等 Hub 排空连接后才停止。
		kratos.Server(hs, server.NewDrainingGRPCServer(gs, hub), hub),
		kratos.StopTimeout(stopTimeout),
		// 路由地址取自注册的 gRPC 端点，必须在开始接受 WebSocket 连接之前确定
		kratos.BeforeStart(routes.ResolveAddr),
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

	app, cleanup, err := wireApp(bc.GetServer(), bc.GetRegistry(), bc.GetRedis(), bc.GetAuth(),
		bc.GetWebsocket(), bc.GetCall(), &bc, policy, logger)
	if err != nil {
		return fmt.Errorf("init app: %w", err)
	}
	defer cleanup()

	return app.Run()
}
