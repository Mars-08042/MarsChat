package bootstrap

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/consul/api"
)

// fakeConsul 模拟 /v1/status/leader：前 noLeader 次返回空串（还没选出 leader），之后返回 leader 地址。
func fakeConsul(t *testing.T, noLeader int32) (*api.Client, *atomic.Int32, *httptest.Server) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/status/leader" {
			http.NotFound(w, r)
			return
		}
		if calls.Add(1) <= noLeader {
			_, _ = w.Write([]byte(`""`))
			return
		}
		_, _ = w.Write([]byte(`"10.0.0.1:8300"`))
	}))
	t.Cleanup(srv.Close)
	cfg := api.DefaultConfig()
	cfg.Address = strings.TrimPrefix(srv.URL, "http://")
	cli, err := api.NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return cli, &calls, srv
}

func textLogger(buf *bytes.Buffer) *slog.Logger { return slog.New(slog.NewTextHandler(buf, nil)) }

func TestWaitForLeader(t *testing.T) {
	t.Run("leader already elected", func(t *testing.T) {
		cli, calls, _ := fakeConsul(t, 0)
		var logs bytes.Buffer
		if err := waitForLeader(context.Background(), cli.Status(), time.Millisecond, textLogger(&logs)); err != nil {
			t.Fatalf("waitForLeader: %v", err)
		}
		if calls.Load() != 1 {
			t.Errorf("calls = %d, want 1", calls.Load())
		}
		if logs.Len() != 0 {
			t.Errorf("unexpected logs: %s", logs.String())
		}
	})

	t.Run("waits until leader elected", func(t *testing.T) {
		cli, calls, _ := fakeConsul(t, 3)
		var logs bytes.Buffer
		if err := waitForLeader(context.Background(), cli.Status(), time.Millisecond, textLogger(&logs)); err != nil {
			t.Fatalf("waitForLeader: %v", err)
		}
		if calls.Load() != 4 {
			t.Errorf("calls = %d, want 4", calls.Load())
		}
		// 等待期间只打一条 warn，等到后一条 info
		out := logs.String()
		if strings.Count(out, "waiting for consul leader") != 1 || strings.Count(out, "consul leader elected") != 1 {
			t.Errorf("logs = %s", out)
		}
	})

	t.Run("no leader before timeout", func(t *testing.T) {
		cli, _, _ := fakeConsul(t, 1<<30)
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		err := waitForLeader(ctx, cli.Status(), 5*time.Millisecond, discardLogger())
		if err == nil || !strings.Contains(err.Error(), "no cluster leader") {
			t.Fatalf("err = %v, want no cluster leader", err)
		}
	})

	t.Run("consul unreachable", func(t *testing.T) {
		cli, _, srv := fakeConsul(t, 0)
		srv.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		err := waitForLeader(ctx, cli.Status(), 5*time.Millisecond, discardLogger())
		if err == nil || !strings.Contains(err.Error(), "connect") {
			t.Fatalf("err = %v, want connection error", err)
		}
	})
}
