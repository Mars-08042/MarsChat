package bootstrap

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Mars-08042/MarsChat/pkg/conf"
)

func TestLoadConfigResolvesEnvPlaceholders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	// Duration 走 protojson 解析，只接受以秒为单位的写法
	content := "addr: ${TEST_REDIS_ADDR:127.0.0.1:16379}\npassword: ${TEST_REDIS_PASSWORD}\ndb: 2\nread_timeout: 0.5s\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvPrefix+"TEST_REDIS_PASSWORD", "secret")

	var c conf.Redis
	if err := LoadConfig(path, &c); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got := c.GetAddr(); got != "127.0.0.1:16379" {
		t.Errorf("addr = %q, want default 127.0.0.1:16379", got)
	}
	if got := c.GetPassword(); got != "secret" {
		t.Errorf("password = %q, want value from %sTEST_REDIS_PASSWORD", got, EnvPrefix)
	}
	if got := c.GetDb(); got != 2 {
		t.Errorf("db = %d, want 2", got)
	}
	if got := c.GetReadTimeout().AsDuration().String(); got != "500ms" {
		t.Errorf("read_timeout = %s, want 500ms", got)
	}
}

func TestLoadConfigEnvOverridesDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("addr: ${TEST_REDIS_ADDR:127.0.0.1:16379}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvPrefix+"TEST_REDIS_ADDR", "redis:6379")

	var c conf.Redis
	if err := LoadConfig(path, &c); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got := c.GetAddr(); got != "redis:6379" {
		t.Errorf("addr = %q, want redis:6379", got)
	}
}
