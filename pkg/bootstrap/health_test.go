package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadinessHandler(t *testing.T) {
	ok := Check{Name: "redis", Fn: func(context.Context) error { return nil }}
	bad := Check{Name: "mysql", Fn: func(context.Context) error { return errors.New("dial tcp 10.0.0.8:3306: connection refused") }}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	tests := []struct {
		name       string
		checks     []Check
		wantCode   int
		wantFailed []string
	}{
		{name: "all pass", checks: []Check{ok}, wantCode: http.StatusOK},
		{name: "one fails", checks: []Check{bad, ok}, wantCode: http.StatusServiceUnavailable, wantFailed: []string{"mysql"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			ReadinessHandler(logger, tt.checks...).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

			if rec.Code != tt.wantCode {
				t.Fatalf("code = %d, want %d", rec.Code, tt.wantCode)
			}
			body := rec.Body.String()
			// 错误详情只进日志，不能出现在响应里
			if strings.Contains(body, "10.0.0.8") {
				t.Errorf("response leaks error detail: %s", body)
			}
			var resp struct {
				Failed []string `json:"failed"`
			}
			if err := json.Unmarshal([]byte(body), &resp); err != nil {
				t.Fatalf("decode body %q: %v", body, err)
			}
			if strings.Join(resp.Failed, ",") != strings.Join(tt.wantFailed, ",") {
				t.Errorf("failed = %v, want %v", resp.Failed, tt.wantFailed)
			}
		})
	}
}
