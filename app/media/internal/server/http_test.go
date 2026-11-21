package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/Mars-08042/MarsChat/app/media/internal/biz"
	"github.com/Mars-08042/MarsChat/app/media/internal/conf"
	"github.com/Mars-08042/MarsChat/app/media/internal/data"
	"github.com/Mars-08042/MarsChat/app/media/internal/service"
	"github.com/Mars-08042/MarsChat/pkg/auth"
	pkgconf "github.com/Mars-08042/MarsChat/pkg/conf"
	"github.com/Mars-08042/MarsChat/pkg/ratelimit"
)

// 本文件用真实的 Kratos HTTP 服务器、中间件链和本地存储，验证自定义处理函数与中间件的衔接：
// 操作名、鉴权白名单、参数校验、错误格式、Range 与签名校验。文件记录用内存实现。

type memRepo struct {
	mu    sync.Mutex
	files map[int64]*biz.File
}

func (r *memRepo) Create(_ context.Context, f *biz.File) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.files[f.ID] = f
	return nil
}

func (r *memRepo) Get(_ context.Context, id int64) (*biz.File, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if f, ok := r.files[id]; ok {
		return f, nil
	}
	return nil, biz.ErrNotFound
}

func (r *memRepo) BatchGet(_ context.Context, ids []int64) (map[int64]*biz.File, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[int64]*biz.File{}
	for _, id := range ids {
		if f, ok := r.files[id]; ok {
			out[id] = f
		}
	}
	return out, nil
}

type seqIDs struct {
	mu   sync.Mutex
	next int64
}

func (g *seqIDs) NextID() (int64, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.next++
	return g.next, nil
}

// syncBuffer 让日志在请求 goroutine 写入、测试 goroutine 读取时不产生数据竞争。
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type testServer struct {
	url   string
	token string
	logs  *syncBuffer
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	signer, err := auth.NewSigner(base64.StdEncoding.EncodeToString(seed), "k1", "marschat-account", "marschat", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := signer.Sign(auth.Principal{UserID: 7, SessionID: 1, Role: 1}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	logs := &syncBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	authConf := &pkgconf.Auth{
		PublicKey: base64.StdEncoding.EncodeToString(signer.PublicKey()),
		Issuer:    "marschat-account",
		Audience:  "marschat",
	}
	mws, err := NewMiddlewares(authConf, rdb, logger)
	if err != nil {
		t.Fatal(err)
	}

	// 地址前缀要在服务器启动后才知道：先占位，再用 httptest 的地址创建存储
	hs := &handlerSwitch{}
	ts := httptest.NewServer(hs)
	t.Cleanup(ts.Close)
	storage, err := data.NewStorage(&conf.Storage{
		Provider: "local",
		Local:    &conf.Local{Dir: t.TempDir(), PublicBaseUrl: ts.URL, UrlSecret: strings.Repeat("s", 32)},
	}, logger)
	if err != nil {
		t.Fatal(err)
	}
	policy := biz.NewPolicyHolder(&conf.Policy{
		MaxChatFileBytes: 1 << 20,
		MaxImageBytes:    1 << 20,
		MaxAvatarBytes:   1 << 20,
		MaxImagePixels:   1_000_000,
		ThumbnailMaxSide: 64,
		DownloadUrlTtl:   durationpb.New(10 * time.Minute),
		UploadLimit:      &pkgconf.RateLimit{Limit: 100, Window: durationpb.New(time.Hour)},
	})
	uc := biz.NewFileUsecase(&memRepo{files: map[int64]*biz.File{}}, storage, biz.NewThumbnailer(), &seqIDs{next: 100},
		ratelimit.New(rdb), policy, logger)
	srv := NewHTTPServer(&pkgconf.Server{Http: &pkgconf.Server_HTTP{Timeout: durationpb.New(30 * time.Second)}},
		mws, data.NewData(nil, rdb), service.NewMediaService(uc), logger)
	hs.h = srv
	return &testServer{url: ts.URL, token: token, logs: logs}
}

type handlerSwitch struct{ h http.Handler }

func (s *handlerSwitch) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.h.ServeHTTP(w, r) }

func (s *testServer) do(t *testing.T, method, path string, body io.Reader, size int64, withToken bool, hdr map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, s.url+path, body)
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = size
	if withToken {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func (s *testServer) upload(t *testing.T, purpose, name string, data []byte) map[string]any {
	t.Helper()
	resp := s.do(t, http.MethodPost, "/v1/files?purpose="+purpose+"&name="+url.QueryEscape(name),
		bytes.NewReader(data), int64(len(data)), true, map[string]string{"Content-Type": "application/octet-stream"})
	out := decode(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload status %d: %v", resp.StatusCode, out)
	}
	return out
}

func (s *testServer) downloadURL(t *testing.T, fileID, accessKey, variant string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"items": []map[string]string{{"fileId": fileID, "accessKey": accessKey, "variant": variant}}})
	resp := s.do(t, http.MethodPost, "/v1/files/download-urls", bytes.NewReader(body), int64(len(body)), true,
		map[string]string{"Content-Type": "application/json"})
	out := decode(t, resp)
	urls, _ := out["urls"].([]any)
	if resp.StatusCode != http.StatusOK || len(urls) != 1 {
		t.Fatalf("download-urls status %d: %v", resp.StatusCode, out)
	}
	return urls[0].(map[string]any)["url"].(string)
}

func decode(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode body (status %d): %v", resp.StatusCode, err)
	}
	return out
}

func get(t *testing.T, rawURL string, hdr map[string]string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, rawURL, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestUploadAndDownloadLocal(t *testing.T) {
	s := newTestServer(t)
	content := []byte(strings.Repeat("MarsChat media ", 100))
	info := s.upload(t, "PURPOSE_CHAT", `C:\docs\说明.txt`, content)
	if info["name"] != "说明.txt" || info["kind"] != "FILE_KIND_FILE" || info["size"] != "1500" || info["uploaderId"] != "7" {
		t.Fatalf("unexpected file info %v", info)
	}
	id, key := info["id"].(string), info["accessKey"].(string)
	if len(key) != 22 {
		t.Fatalf("access key %q", key)
	}

	raw := s.downloadURL(t, id, key, "VARIANT_ORIGINAL")
	// 下载地址不需要访问令牌
	resp := get(t, raw, nil)
	got, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !bytes.Equal(got, content) {
		t.Fatalf("download status %d, %d bytes", resp.StatusCode, len(got))
	}
	h := resp.Header
	if h.Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(h.Get("Content-Disposition"), "attachment;") ||
		!strings.Contains(h.Get("Content-Disposition"), "filename*=UTF-8''%E8%AF%B4%E6%98%8E.txt") ||
		h.Get("X-Content-Type-Options") != "nosniff" || !strings.HasPrefix(h.Get("Cache-Control"), "private, max-age=") ||
		h.Get("Accept-Ranges") != "bytes" {
		t.Fatalf("unexpected headers %v", h)
	}

	// Range
	resp = get(t, raw, map[string]string{"Range": "bytes=15-29"})
	got, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusPartialContent || string(got) != "MarsChat media " {
		t.Fatalf("range status %d body %q", resp.StatusCode, got)
	}

	// 篡改签名或文件 ID
	u, _ := url.Parse(raw)
	q := u.Query()
	q.Set("signature", strings.Repeat("A", 43))
	u.RawQuery = q.Encode()
	resp = get(t, u.String(), nil)
	if out := decode(t, resp); resp.StatusCode != http.StatusForbidden || out["reason"] != "DOWNLOAD_URL_INVALID" {
		t.Fatalf("tampered: %d %v", resp.StatusCode, out)
	}
	resp = get(t, strings.Replace(raw, "/v1/files/"+id+"/", "/v1/files/999/", 1), nil)
	if out := decode(t, resp); resp.StatusCode != http.StatusForbidden || out["reason"] != "DOWNLOAD_URL_INVALID" {
		t.Fatalf("other id: %d %v", resp.StatusCode, out)
	}
	// 缺参数由 protovalidate 拒绝
	resp = get(t, s.url+"/v1/files/"+id+"/content", nil)
	if out := decode(t, resp); resp.StatusCode != http.StatusBadRequest || out["reason"] != "VALIDATOR" {
		t.Fatalf("missing params: %d %v", resp.StatusCode, out)
	}

	// 访问日志使用自定义处理函数的操作名
	logs := s.logs.String()
	for _, op := range []string{service.OperationUploadFile, service.OperationDownloadContent, "/media.v1.MediaService/GetDownloadUrls"} {
		if !strings.Contains(logs, "operation="+op) {
			t.Errorf("access log missing operation %s", op)
		}
	}
}

func TestUploadImageThumbnail(t *testing.T) {
	s := newTestServer(t)
	img := image.NewNRGBA(image.Rect(0, 0, 200, 100))
	for i := range img.Pix {
		img.Pix[i] = 0x80
	}
	img.Set(0, 0, color.Black)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	info := s.upload(t, "PURPOSE_CHAT", "pic.png", buf.Bytes())
	if info["kind"] != "FILE_KIND_IMAGE" || info["hasThumbnail"] != true || info["width"] != float64(200) {
		t.Fatalf("unexpected file info %v", info)
	}
	raw := s.downloadURL(t, info["id"].(string), info["accessKey"].(string), "VARIANT_THUMBNAIL")
	resp := get(t, raw, nil)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "image/jpeg" ||
		!strings.HasPrefix(resp.Header.Get("Content-Disposition"), `inline; filename="pic.jpg"`) {
		t.Fatalf("thumbnail: %d %v", resp.StatusCode, resp.Header)
	}
	cfg, format, err := image.DecodeConfig(resp.Body)
	if err != nil || format != "jpeg" || cfg.Width != 64 || cfg.Height != 32 {
		t.Fatalf("thumbnail %s %dx%d %v", format, cfg.Width, cfg.Height, err)
	}
}

func TestUploadRejections(t *testing.T) {
	s := newTestServer(t)
	data := []byte("hello")
	cases := []struct {
		name      string
		query     string
		body      io.Reader
		size      int64
		withToken bool
		status    int
		reason    string
	}{
		{"no token", "?purpose=PURPOSE_CHAT", bytes.NewReader(data), 5, false, 401, "UNAUTHORIZED"},
		{"missing purpose", "", bytes.NewReader(data), 5, true, 400, "VALIDATOR"},
		{"unknown purpose", "?purpose=PURPOSE_FOO", bytes.NewReader(data), 5, true, 400, "CODEC"},
		// ContentLength = -1 时客户端用 chunked 编码，服务端拿不到长度
		{"no content length", "?purpose=PURPOSE_CHAT", io.MultiReader(bytes.NewReader(data)), -1, true, 411, "LENGTH_REQUIRED"},
		{"empty", "?purpose=PURPOSE_CHAT", http.NoBody, 0, true, 400, "VALIDATOR"},
		{"avatar not image", "?purpose=PURPOSE_AVATAR", bytes.NewReader(data), 5, true, 415, "UNSUPPORTED_FILE_TYPE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := s.do(t, http.MethodPost, "/v1/files"+tc.query, tc.body, tc.size, tc.withToken, nil)
			out := decode(t, resp)
			if resp.StatusCode != tc.status || out["reason"] != tc.reason {
				t.Fatalf("got %d %v, want %d %s", resp.StatusCode, out, tc.status, tc.reason)
			}
		})
	}
}
