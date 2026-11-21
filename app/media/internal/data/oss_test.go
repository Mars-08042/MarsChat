package data

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"

	"github.com/Mars-08042/MarsChat/app/media/internal/biz"
	"github.com/Mars-08042/MarsChat/app/media/internal/conf"
	"github.com/Mars-08042/MarsChat/pkg/idgen"
)

func testOSSConf() *conf.Oss {
	return &conf.Oss{
		Region:          "cn-hangzhou",
		Bucket:          "marschat-test",
		AccessKeyId:     "test-ak",
		AccessKeySecret: "test-sk",
		KeyPrefix:       "marschat/",
	}
}

func TestOSSConfigValidation(t *testing.T) {
	c := testOSSConf()
	c.Bucket, c.AccessKeySecret = "", ""
	_, err := newOSSStorage(c)
	if err == nil || !strings.Contains(err.Error(), "bucket") || !strings.Contains(err.Error(), "access_key_secret") {
		t.Fatalf("err = %v", err)
	}
}

// idAt 构造在 at 时刻生成的 snowflake ID（节点号、序号为 0），并用 idgen.Decompose 核对位布局。
func idAt(t *testing.T, at time.Time) int64 {
	t.Helper()
	const epochMs, timeShift = 1767225600000, 22
	id := (at.UnixMilli() - epochMs) << timeShift
	if got, _, _ := idgen.Decompose(id); !got.Equal(at) {
		t.Fatalf("idgen layout changed: Decompose(%d) = %v, want %v", id, got, at)
	}
	return id
}

// 对象名按 ID 中的生成时间（UTC）分年、月目录，原文件与缩略图各自分开。
func TestOSSObjectName(t *testing.T) {
	s, err := newOSSStorage(testOSSConf())
	if err != nil {
		t.Fatal(err)
	}
	lastNov := idAt(t, time.Date(2026, 11, 30, 23, 59, 59, 999e6, time.UTC))
	firstDec := idAt(t, time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC))
	cases := []struct {
		key  string
		want string
	}{
		{biz.ObjectKey(lastNov, biz.VariantOriginal), "marschat/files/2026/11/" + strconv.FormatInt(lastNov, 10)},
		{biz.ObjectKey(lastNov, biz.VariantThumbnail), "marschat/thumbs/2026/11/" + strconv.FormatInt(lastNov, 10) + ".jpg"},
		{biz.ObjectKey(firstDec, biz.VariantOriginal), "marschat/files/2026/12/" + strconv.FormatInt(firstDec, 10)},
	}
	for _, c := range cases {
		got, err := s.objectName(c.key)
		if err != nil || got != c.want {
			t.Errorf("objectName(%q) = %q, %v; want %q", c.key, got, err, c.want)
		}
	}
	for _, bad := range []string{"", "42", "files/", "files/abc", "files/-1", "files/0.jpg"} {
		if got, err := s.objectName(bad); err == nil {
			t.Errorf("objectName(%q) = %q, want error", bad, got)
		}
	}
}

// 预签名只做本地计算，不需要网络。
func TestOSSPresign(t *testing.T) {
	s, err := newOSSStorage(testOSSConf())
	if err != nil {
		t.Fatal(err)
	}
	obj := biz.Object{
		Key:                biz.ObjectKey(42, biz.VariantOriginal),
		ContentType:        "application/octet-stream",
		ContentDisposition: biz.ContentDisposition("attachment", "报告.pdf"),
	}
	raw, err := s.SignURL(context.Background(), obj, time.Now().Add(10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "https" || u.Host != "marschat-test.oss-cn-hangzhou.aliyuncs.com" || u.Path != "/marschat/files/2026/01/42" {
		t.Fatalf("unexpected url %s", raw)
	}
	q := u.Query()
	// OSS 拒绝 GET 覆盖 Content-Type，类型由上传时的对象元数据决定
	if q.Has("response-content-type") || q.Get("response-content-disposition") != obj.ContentDisposition {
		t.Fatalf("unexpected response overrides: %v", q)
	}
	if q.Get("x-oss-signature-version") != "OSS4-HMAC-SHA256" || q.Get("x-oss-signature") == "" {
		t.Fatalf("not a V4 signature: %v", q)
	}
	if exp := q.Get("x-oss-expires"); exp != "600" && exp != "599" {
		t.Fatalf("x-oss-expires = %s", exp)
	}
	if strings.Contains(raw, "test-sk") {
		t.Fatal("secret leaked into url")
	}

	t.Run("custom domain", func(t *testing.T) {
		c := testOSSConf()
		c.PublicEndpoint = "https://files.example.com"
		s, err := newOSSStorage(c)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := s.SignURL(context.Background(), obj, time.Now().Add(10*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(raw)
		if u.Host != "files.example.com" || u.Path != "/marschat/files/2026/01/42" {
			t.Fatalf("unexpected url %s", raw)
		}
	})
}

// fakeOSS 记录收到的请求，按 path-style 解析 Bucket 与对象键（SDK 对 IP 地址的 endpoint 自动使用 path-style）。
type fakeOSS struct {
	mu       sync.Mutex
	requests []recorded
	status   int
}

type recorded struct {
	method, path, contentType, auth string
	contentLength                   int64
	body                            string
}

func (f *fakeOSS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.requests = append(f.requests, recorded{
		method: r.Method, path: r.URL.Path, contentType: r.Header.Get("Content-Type"),
		auth: r.Header.Get("Authorization"), contentLength: r.ContentLength, body: string(body),
	})
	status := f.status
	f.mu.Unlock()
	w.Header().Set("x-oss-request-id", "test-request")
	if status != 0 {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>AccessDenied</Code><Message>denied</Message><RequestId>test-request</RequestId></Error>`)
		return
	}
	if r.Method == http.MethodDelete {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("ETag", `"etag"`)
	w.WriteHeader(http.StatusOK)
}

func newFakeOSSStorage(t *testing.T, f *fakeOSS) *ossStorage {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c := testOSSConf()
	c.Endpoint = srv.URL
	s, err := newOSSStorage(c)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestOSSPutAndDelete(t *testing.T) {
	f := &fakeOSS{}
	s := newFakeOSSStorage(t, f)
	ctx := context.Background()
	// 不可 Seek 的流式 body，与上传普通文件时一致
	body := io.MultiReader(strings.NewReader("hello "), strings.NewReader("oss"))
	if err := s.Put(ctx, "files/42", body, 9, "image/png"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "files/42"); err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 2 {
		t.Fatalf("requests: %+v", f.requests)
	}
	put, del := f.requests[0], f.requests[1]
	if put.method != http.MethodPut || put.path != "/marschat-test/marschat/files/2026/01/42" || put.body != "hello oss" ||
		put.contentLength != 9 || put.contentType != "image/png" || !strings.HasPrefix(put.auth, "OSS4-HMAC-SHA256 ") {
		t.Fatalf("unexpected put %+v", put)
	}
	if del.method != http.MethodDelete || del.path != "/marschat-test/marschat/files/2026/01/42" {
		t.Fatalf("unexpected delete %+v", del)
	}
}

func TestOSSServiceError(t *testing.T) {
	f := &fakeOSS{status: http.StatusForbidden}
	s := newFakeOSSStorage(t, f)
	err := s.Put(context.Background(), "files/1", strings.NewReader("x"), 1, "application/octet-stream")
	var serr *oss.ServiceError
	if !errors.As(err, &serr) || serr.Code != "AccessDenied" || serr.StatusCode != http.StatusForbidden {
		t.Fatalf("err = %v", err)
	}
}
