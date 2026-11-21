package bootstrap

import (
	"log/slog"
	"os"

	"github.com/go-kratos/kratos/v3/log"

	"github.com/Mars-08042/MarsChat/pkg/conf"
	"github.com/Mars-08042/MarsChat/pkg/telemetry"
)

// NewLogger 按配置创建日志器，并设为 Kratos 的默认日志器。attrs 会附加到每条日志上；
// 带 ctx 记录且 ctx 中有 Span 时还会附加 trace_id、span_id（telemetry.LogHandler）。
func NewLogger(c *conf.Log, attrs ...slog.Attr) *slog.Logger {
	format := log.FormatText
	if c.GetFormat() == "json" {
		format = log.FormatJSON
	}
	handler := telemetry.LogHandler(log.NewHandler(
		log.WithWriter(os.Stdout),
		log.WithFormat(format),
		// 无法识别的级别按 info 处理
		log.WithLevel(log.ParseLevel(c.GetLevel())),
	))
	args := make([]any, 0, len(attrs))
	for _, a := range attrs {
		args = append(args, a)
	}
	logger := slog.New(handler).With(args...)
	log.SetDefault(logger)
	return logger
}
