package data

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Mars-08042/MarsChat/app/media/internal/biz"
	"github.com/Mars-08042/MarsChat/app/media/internal/conf"
)

const testSecret = "0123456789abcdef0123456789abcdef"

func newTestLocal(t *testing.T) *localStorage {
	t.Helper()
	s, err := newLocalStorage(&conf.Local{Dir: t.TempDir(), PublicBaseUrl: "http://127.0.0.1:18050/", UrlSecret: testSecret})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestLocalConfigValidation(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]*conf.Local{
		"missing dir":    {PublicBaseUrl: "http://127.0.0.1:18050", UrlSecret: testSecret},
		"short secret":   {Dir: dir, PublicBaseUrl: "http://127.0.0.1:18050", UrlSecret: "short"},
		"relative url":   {Dir: dir, PublicBaseUrl: "127.0.0.1:18050", UrlSecret: testSecret},
		"ftp url":        {Dir: dir, PublicBaseUrl: "ftp://127.0.0.1", UrlSecret: testSecret},
		"url with query": {Dir: dir, PublicBaseUrl: "http://127.0.0.1:18050/?a=1", UrlSecret: testSecret},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := newLocalStorage(c); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestLocalPutOpenDelete(t *testing.T) {
	s := newTestLocal(t)
	ctx := context.Background()
	data := bytes.Repeat([]byte("0123456789"), 1000)
	key := biz.ObjectKey(123456789, biz.VariantOriginal)
	if err := s.Put(ctx, key, bytes.NewReader(data), int64(len(data)), "application/octet-stream"); err != nil {
		t.Fatal(err)
	}
	p, _ := s.path(key)
	if rel, _ := filepath.Rel(s.dir, p); !strings.HasPrefix(filepath.ToSlash(rel), "files/") || len(strings.Split(filepath.ToSlash(rel), "/")) != 3 {
		t.Fatalf("unexpected layout %s", rel)
	}

	c, err := s.Open(key)
	if err != nil {
		t.Fatal(err)
	}
	// Range 请求依赖 Seek
	if _, err := c.Seek(5, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(c)
	_ = c.Close()
	if !bytes.Equal(got, data[5:]) || c.ModTime().IsZero() {
		t.Fatal("content mismatch")
	}

	if err := s.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Open(key); !errors.Is(err, biz.ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
	// 重复删除不报错
	if err := s.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
}

func TestLocalPutFailureLeavesNothing(t *testing.T) {
	s := newTestLocal(t)
	key := biz.ObjectKey(1, biz.VariantOriginal)
	cases := map[string]struct {
		r    io.Reader
		size int64
	}{
		"short body":   {strings.NewReader("abc"), 10},
		"reader error": {io.MultiReader(strings.NewReader("abc"), errReader{}), 10},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if err := s.Put(context.Background(), key, tc.r, tc.size, ""); err == nil {
				t.Fatal("expected error")
			}
			if _, err := s.Open(key); !errors.Is(err, biz.ErrNotFound) {
				t.Fatalf("partial object visible: %v", err)
			}
			entries, _ := os.ReadDir(s.tmpDir())
			if len(entries) != 0 {
				t.Fatalf("temp files left: %v", entries)
			}
		})
	}
	t.Run("canceled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := s.Put(ctx, key, strings.NewReader("abc"), 3, ""); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	})
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

func TestLocalRejectsBadKeys(t *testing.T) {
	s := newTestLocal(t)
	for _, key := range []string{"", "../x", "files/../../x", "/abs", `files\x`} {
		if err := s.Put(context.Background(), key, strings.NewReader("a"), 1, ""); err == nil {
			t.Errorf("key %q accepted", key)
		}
	}
}

func TestLocalSignedURL(t *testing.T) {
	s := newTestLocal(t)
	exp := time.Unix(1_900_000_000, 0)
	raw, err := s.SignURL(context.Background(), biz.Object{FileID: 42, Variant: biz.VariantThumbnail}, exp)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme+"://"+u.Host+u.Path != "http://127.0.0.1:18050/v1/files/42/content" {
		t.Fatalf("unexpected url %s", raw)
	}
	q := u.Query()
	variant, _ := strconv.Atoi(q.Get("variant"))
	expires, _ := strconv.ParseInt(q.Get("expires"), 10, 64)
	sig := q.Get("signature")
	if variant != int(biz.VariantThumbnail) || expires != exp.Unix() || len(sig) != 43 {
		t.Fatalf("query %v", q)
	}
	if !s.VerifyURL(42, biz.VariantThumbnail, expires, sig) {
		t.Fatal("valid signature rejected")
	}
	tampered := []struct {
		id      int64
		variant int32
		exp     int64
		sig     string
	}{
		{43, biz.VariantThumbnail, expires, sig},
		{42, biz.VariantOriginal, expires, sig},
		{42, biz.VariantThumbnail, expires + 1, sig},
		{42, biz.VariantThumbnail, expires, sig[:42] + "A"},
		{42, biz.VariantThumbnail, expires, ""},
	}
	for _, c := range tampered {
		if s.VerifyURL(c.id, c.variant, c.exp, c.sig) {
			t.Errorf("tampered url accepted: %+v", c)
		}
	}
	// 不同密钥的实例不接受
	other, err := newLocalStorage(&conf.Local{Dir: t.TempDir(), PublicBaseUrl: "http://x", UrlSecret: strings.Repeat("z", 32)})
	if err != nil {
		t.Fatal(err)
	}
	if other.VerifyURL(42, biz.VariantThumbnail, expires, sig) {
		t.Fatal("signature accepted with a different secret")
	}
}
