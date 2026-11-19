package conf_test

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Mars-08042/MarsChat/app/account/internal/conf"
	"github.com/Mars-08042/MarsChat/pkg/bootstrap"
)

// 密钥只从环境变量读取，测试里填占位值让校验通过；这里只检查配置能否解析，不使用密钥。
func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv(bootstrap.EnvPrefix+"JWT_PUBLIC_KEY", "test-public-key")
	t.Setenv(bootstrap.EnvPrefix+"JWT_PRIVATE_KEY", "test-private-key")
}

// 确保仓库里的配置文件能按 protojson 规则解析，例如时长不能写成 500ms。
func TestConfigFileParses(t *testing.T) {
	setRequiredEnv(t)
	var bc conf.Bootstrap
	c, err := bootstrap.LoadConfig("../../configs", &bc)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	defer c.Close()
	if bc.GetServer().GetGrpc().GetAddr() == "" || bc.GetMysql().GetDsn() == "" || bc.GetRedis().GetAddr() == "" {
		// 不打印整个配置，DSN 里可能带密码
		t.Error("config not fully loaded: server.grpc.addr, mysql.dsn or redis.addr is empty")
	}
	if got := bc.GetPolicy().GetSms().GetDailyLimitPerPhone(); got != 10 {
		t.Errorf("policy.sms.daily_limit_per_phone = %d, want 10", got)
	}
	if bc.GetToken().GetRevocationTtl().AsDuration() < bc.GetToken().GetAccessTtl().AsDuration() {
		t.Error("token.revocation_ttl must cover token.access_ttl")
	}
}

// 修改配置文件中的 policy 段后自动生效；改成非法值时保留旧值。
func TestPolicyHotReload(t *testing.T) {
	setRequiredEnv(t)
	src, err := os.ReadFile("../../configs/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}

	var bc conf.Bootstrap
	c, err := bootstrap.LoadConfig(path, &bc)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	defer c.Close()

	applied := make(chan *conf.Policy, 4)
	logs := &lockedBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, nil))
	if err := bootstrap.WatchConfig(c, "policy", func() *conf.Policy { return &conf.Policy{} },
		func(p *conf.Policy) { applied <- p }, logger); err != nil {
		t.Fatalf("WatchConfig: %v", err)
	}

	rewrite := func(from, to string) {
		t.Helper()
		content := strings.Replace(string(src), from, to, 1)
		if content == string(src) {
			t.Fatalf("%q not found in config", from)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	rewrite("daily_limit_per_phone: 10", "daily_limit_per_phone: 3")
	select {
	case p := <-applied:
		if got := p.GetSms().GetDailyLimitPerPhone(); got != 3 {
			t.Fatalf("reloaded daily_limit_per_phone = %d, want 3", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("policy not reloaded within 5s")
	}

	// 非法值（必须大于 0）不应用
	rewrite("daily_limit_per_phone: 10", "daily_limit_per_phone: 0")
	select {
	case p := <-applied:
		t.Fatalf("invalid policy applied: daily_limit_per_phone = %d", p.GetSms().GetDailyLimitPerPhone())
	case <-time.After(2 * time.Second):
	}
	// 确认确实触发了重新加载并被校验拦下，而不是根本没有收到文件变化
	if !strings.Contains(logs.String(), "reloaded config is invalid") {
		t.Fatalf("invalid reload not logged, logs: %s", logs.String())
	}
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
