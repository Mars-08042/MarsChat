package bootstrap

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

// readinessTimeout 是单次就绪检查的总超时，要小于探针的超时设置
const readinessTimeout = 2 * time.Second

// Check 是一项依赖检查，返回 nil 表示依赖可用。
type Check struct {
	Name string
	Fn   func(ctx context.Context) error
}

// LivenessHandler 只表示进程存活，不检查依赖。
// 依赖故障时重启进程解决不了问题，所以存活探针不能依赖外部组件。
func LivenessHandler(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// ReadinessHandler 依次执行依赖检查，全部通过返回 200，否则返回 503 和失败的依赖名。
// 错误详情只写日志，不放进响应，避免暴露内部地址。
func ReadinessHandler(logger *slog.Logger, checks ...Check) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
		defer cancel()

		failed := make([]string, 0)
		for _, c := range checks {
			if err := c.Fn(ctx); err != nil {
				failed = append(failed, c.Name)
				logger.Warn("readiness check failed", slog.String("dependency", c.Name), slog.Any("error", err))
			}
		}
		if len(failed) > 0 {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "unavailable", "failed": failed})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
