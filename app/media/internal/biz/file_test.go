package biz

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"image"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	kerrors "github.com/go-kratos/kratos/v3/errors"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/Mars-08042/MarsChat/app/media/internal/conf"
	pkgconf "github.com/Mars-08042/MarsChat/pkg/conf"
	"github.com/Mars-08042/MarsChat/pkg/idgen"
	"github.com/Mars-08042/MarsChat/pkg/ratelimit"
)

// ---- fakes ----

type memRepo struct {
	mu        sync.Mutex
	files     map[int64]*File
	createErr error
}

func newMemRepo() *memRepo { return &memRepo{files: map[int64]*File{}} }

func (r *memRepo) Create(_ context.Context, f *File) error {
	if r.createErr != nil {
		return r.createErr
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *f
	r.files[f.ID] = &cp
	return nil
}

func (r *memRepo) Get(_ context.Context, id int64) (*File, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if f, ok := r.files[id]; ok {
		return f, nil
	}
	return nil, ErrNotFound
}

func (r *memRepo) BatchGet(_ context.Context, ids []int64) (map[int64]*File, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[int64]*File{}
	for _, id := range ids {
		if f, ok := r.files[id]; ok {
			out[id] = f
		}
	}
	return out, nil
}

type memStorage struct {
	mu      sync.Mutex
	kind    int32
	objects map[string][]byte
	types   map[string]string
	putErr  error
	deleted []string
}

func newMemStorage() *memStorage {
	return &memStorage{kind: StorageLocal, objects: map[string][]byte{}, types: map[string]string{}}
}

func (s *memStorage) Kind() int32 { return s.kind }

func (s *memStorage) Put(_ context.Context, key string, r io.Reader, size int64, contentType string) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if s.putErr != nil {
		return s.putErr
	}
	if int64(len(data)) != size {
		return errors.New("size mismatch")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key] = data
	s.types[key] = contentType
	return nil
}

func (s *memStorage) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objects, key)
	s.deleted = append(s.deleted, key)
	return nil
}

func (s *memStorage) SignURL(_ context.Context, obj Object, _ time.Time) (string, error) {
	return "signed://" + obj.Key + "?ct=" + obj.ContentType, nil
}

type seqIDs struct {
	next int64
	err  error
}

func (g *seqIDs) NextID() (int64, error) {
	if g.err != nil {
		return 0, g.err
	}
	g.next++
	return g.next, nil
}

type fakeLimiter struct {
	res ratelimit.Result
	err error
}

func (l *fakeLimiter) Allow(context.Context, string, int, time.Duration) (ratelimit.Result, error) {
	return l.res, l.err
}

func testPolicy() *conf.Policy {
	return &conf.Policy{
		MaxChatFileBytes: 1 << 20,
		MaxImageBytes:    64 << 10,
		MaxAvatarBytes:   32 << 10,
		MaxImagePixels:   1_000_000,
		ThumbnailMaxSide: 64,
		DownloadUrlTtl:   durationpb.New(10 * time.Minute),
		UploadLimit:      &pkgconf.RateLimit{Limit: 60, Window: durationpb.New(time.Hour)},
	}
}

type fixture struct {
	uc      *FileUsecase
	repo    *memRepo
	storage *memStorage
	ids     *seqIDs
	limiter *fakeLimiter
}

func newFixture() *fixture {
	f := &fixture{repo: newMemRepo(), storage: newMemStorage(), ids: &seqIDs{next: 1000},
		limiter: &fakeLimiter{res: ratelimit.Result{Allowed: true}}}
	f.uc = NewFileUsecase(f.repo, f.storage, NewThumbnailer(), f.ids, f.limiter,
		NewPolicyHolder(testPolicy()), slog.New(slog.DiscardHandler))
	return f
}

func (f *fixture) upload(purpose int32, name string, data []byte) (*File, error) {
	return f.uc.Upload(context.Background(), UploadInput{
		UploaderID: 7, Purpose: purpose, Name: name, Size: int64(len(data)), Body: bytes.NewReader(data),
	})
}

func reason(err error) string { return kerrors.FromError(err).GetReason() }

// ---- upload ----

func TestUploadPlainFile(t *testing.T) {
	fx := newFixture()
	data := []byte("hello, media\n")
	f, err := fx.upload(PurposeChat, "dir/notes.txt", data)
	if err != nil {
		t.Fatal(err)
	}
	if f.Kind != KindFile || f.Name != "notes.txt" || f.Size != int64(len(data)) || f.MimeType != "text/plain; charset=utf-8" {
		t.Fatalf("unexpected file %+v", f)
	}
	if sum := sha256.Sum256(data); !bytes.Equal(f.SHA256, sum[:]) {
		t.Fatal("sha256 mismatch")
	}
	if len(f.AccessKey) != AccessKeySize || f.Storage != StorageLocal || f.UploaderID != 7 {
		t.Fatalf("unexpected key/storage/uploader: %+v", f)
	}
	key := ObjectKey(f.ID, VariantOriginal)
	if !bytes.Equal(fx.storage.objects[key], data) {
		t.Fatal("stored bytes differ")
	}
	// 非图片以 octet-stream 写入存储，OSS 上的对象即使被直接访问也不会内联显示
	if fx.storage.types[key] != "application/octet-stream" {
		t.Fatalf("stored content type = %q", fx.storage.types[key])
	}
	if _, ok := fx.storage.objects[ObjectKey(f.ID, VariantThumbnail)]; ok {
		t.Fatal("plain file must not have thumbnail")
	}
	if _, err := fx.repo.Get(context.Background(), f.ID); err != nil {
		t.Fatal("record not created")
	}
}

// 读入内存的图片预算被占满时返回 503，不读取请求体、不写存储；普通文件流式写入，不受预算影响。
func TestUploadImageBufferBudgetExhausted(t *testing.T) {
	fx := newFixture()
	fx.uc.thumbs = newThumbnailer(1, 1024, 20*time.Millisecond)
	hold, err := fx.uc.thumbs.ReserveBuffer(context.Background(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer hold()

	if _, err := fx.upload(PurposeChat, "cat.png", encodePNG(t, 20, 20)); kerrors.FromError(err).GetCode() != 503 {
		t.Fatalf("image upload: err = %v, want 503", err)
	}
	if len(fx.storage.objects) != 0 {
		t.Fatalf("objects written: %d", len(fx.storage.objects))
	}
	if _, err := fx.upload(PurposeChat, "notes.txt", []byte("plain text")); err != nil {
		t.Fatalf("plain file upload: %v", err)
	}
}

func TestUploadImageCreatesThumbnail(t *testing.T) {
	fx := newFixture()
	data := encodePNG(t, 200, 100)
	f, err := fx.upload(PurposeChat, "cat.png", data)
	if err != nil {
		t.Fatal(err)
	}
	if f.Kind != KindImage || f.Width != 200 || f.Height != 100 || !f.HasThumbnail() {
		t.Fatalf("unexpected file %+v", f)
	}
	thumb := fx.storage.objects[ObjectKey(f.ID, VariantThumbnail)]
	if int32(len(thumb)) != f.ThumbnailSize {
		t.Fatalf("thumbnail size %d, record %d", len(thumb), f.ThumbnailSize)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(thumb))
	if err != nil || format != "jpeg" || cfg.Width != 64 || cfg.Height != 32 {
		t.Fatalf("thumbnail %s %dx%d err %v", format, cfg.Width, cfg.Height, err)
	}
	if fx.storage.types[ObjectKey(f.ID, VariantOriginal)] != "image/png" {
		t.Fatal("decoded image should be stored with its own type")
	}
	if sum := sha256.Sum256(data); !bytes.Equal(f.SHA256, sum[:]) {
		t.Fatal("sha256 mismatch")
	}
}

func TestUploadImageFallsBackToFile(t *testing.T) {
	t.Run("larger than image limit", func(t *testing.T) {
		fx := newFixture()
		data := noisePNG(t, 200, 200) // 随机像素几乎不可压缩，超过 64 KiB
		if len(data) <= int(testPolicy().GetMaxImageBytes()) {
			t.Fatalf("fixture too small: %d", len(data))
		}
		f, err := fx.upload(PurposeChat, "big.png", data)
		if err != nil {
			t.Fatal(err)
		}
		if f.Kind != KindFile || f.MimeType != "image/png" || f.HasThumbnail() {
			t.Fatalf("unexpected file %+v", f)
		}
	})
	t.Run("corrupt", func(t *testing.T) {
		fx := newFixture()
		data := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{1}, 50)...)
		f, err := fx.upload(PurposeChat, "bad.png", data)
		if err != nil {
			t.Fatal(err)
		}
		if f.Kind != KindFile || f.HasThumbnail() {
			t.Fatalf("unexpected file %+v", f)
		}
	})
}

func TestUploadAvatarMustBeImage(t *testing.T) {
	fx := newFixture()
	if _, err := fx.upload(PurposeAvatar, "a.txt", []byte("not an image")); reason(err) != "UNSUPPORTED_FILE_TYPE" {
		t.Fatalf("text avatar: err = %v", err)
	}
	corrupt := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{1}, 50)...)
	if _, err := fx.upload(PurposeAvatar, "bad.png", corrupt); reason(err) != "UNSUPPORTED_FILE_TYPE" {
		t.Fatalf("corrupt avatar: err = %v", err)
	}
	if len(fx.storage.objects) != 0 {
		t.Fatal("rejected avatars must not be stored")
	}
	f, err := fx.upload(PurposeAvatar, "me.jpg", encodeJPEG(t, 100, 100))
	if err != nil || f.Kind != KindImage || f.Purpose != PurposeAvatar {
		t.Fatalf("jpeg avatar: %+v %v", f, err)
	}
}

func TestUploadSizeChecks(t *testing.T) {
	fx := newFixture()
	ctx := context.Background()
	_, err := fx.uc.Upload(ctx, UploadInput{UploaderID: 7, Purpose: PurposeChat, Size: -1, Body: strings.NewReader("x")})
	if reason(err) != "LENGTH_REQUIRED" || kerrors.FromError(err).GetCode() != 411 {
		t.Fatalf("unknown length: %v", err)
	}
	_, err = fx.uc.Upload(ctx, UploadInput{UploaderID: 7, Purpose: PurposeChat, Size: 0, Body: strings.NewReader("")})
	if reason(err) != "VALIDATOR" {
		t.Fatalf("empty: %v", err)
	}
	// 超限在读请求体之前拒绝
	body := &countingReader{}
	_, err = fx.uc.Upload(ctx, UploadInput{UploaderID: 7, Purpose: PurposeAvatar, Size: 32<<10 + 1, Body: body})
	e := kerrors.FromError(err)
	if e.GetCode() != 413 || e.GetMetadata()["max_bytes"] != "32768" || body.n != 0 {
		t.Fatalf("too large: %v metadata %v read %d", err, e.GetMetadata(), body.n)
	}
}

type countingReader struct{ n int }

func (c *countingReader) Read(p []byte) (int, error) {
	c.n += len(p)
	return len(p), nil
}

func TestUploadIncompleteBody(t *testing.T) {
	cases := map[string][]byte{
		"plain": bytes.Repeat([]byte("a"), 2000),
		"image": encodePNG(t, 50, 50),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			fx := newFixture()
			// 声明的长度比实际多，模拟客户端中途断开
			_, err := fx.uc.Upload(context.Background(), UploadInput{
				UploaderID: 7, Purpose: PurposeChat, Name: "x", Size: int64(len(data)) + 10, Body: bytes.NewReader(data),
			})
			if reason(err) != "UPLOAD_INCOMPLETE" {
				t.Fatalf("err = %v", err)
			}
			if len(fx.storage.objects) != 0 || len(fx.repo.files) != 0 {
				t.Fatal("incomplete upload must leave nothing behind")
			}
		})
	}
}

func TestUploadStorageFailure(t *testing.T) {
	fx := newFixture()
	fx.storage.putErr = errors.New("oss unreachable")
	_, err := fx.upload(PurposeChat, "a.txt", []byte("data"))
	if reason(err) != "DEPENDENCY_UNAVAILABLE" || kerrors.FromError(err).GetCode() != 503 {
		t.Fatalf("err = %v", err)
	}
}

func TestUploadRecordFailureDeletesObjects(t *testing.T) {
	fx := newFixture()
	fx.repo.createErr = errors.New("mysql down")
	_, err := fx.upload(PurposeChat, "cat.png", encodePNG(t, 100, 100))
	if err == nil {
		t.Fatal("expected error")
	}
	if len(fx.storage.objects) != 0 || len(fx.storage.deleted) != 2 {
		t.Fatalf("objects left %v, deleted %v", fx.storage.objects, fx.storage.deleted)
	}
}

func TestUploadRateLimit(t *testing.T) {
	fx := newFixture()
	fx.limiter.res = ratelimit.Result{Allowed: false, RetryAfter: 1500 * time.Millisecond}
	_, err := fx.upload(PurposeChat, "a.txt", []byte("data"))
	e := kerrors.FromError(err)
	if e.GetReason() != "RATE_LIMITED" || e.GetMetadata()["retry_after_seconds"] != "2" {
		t.Fatalf("err = %v", err)
	}
	// Redis 故障时放行
	fx.limiter.err = errors.New("redis down")
	if _, err := fx.upload(PurposeChat, "a.txt", []byte("data")); err != nil {
		t.Fatalf("limiter failure should fail open: %v", err)
	}
}

func TestUploadIDLeaseLost(t *testing.T) {
	fx := newFixture()
	fx.ids.err = idgen.ErrLeaseLost
	if _, err := fx.upload(PurposeChat, "a.txt", []byte("data")); reason(err) != "DEPENDENCY_UNAVAILABLE" {
		t.Fatalf("err = %v", err)
	}
}

// ---- download ----

func TestDownloadURLsPermissions(t *testing.T) {
	fx := newFixture()
	img, err := fx.upload(PurposeChat, "cat.png", encodePNG(t, 100, 100))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := fx.upload(PurposeChat, "a.txt", []byte("data"))
	if err != nil {
		t.Fatal(err)
	}
	avatar, err := fx.upload(PurposeAvatar, "me.png", encodePNG(t, 20, 20))
	if err != nil {
		t.Fatal(err)
	}
	// 写在另一种存储中的文件（切换存储之前上传的）
	old := *doc
	old.ID, old.Storage = 9999, StorageOSS
	fx.repo.files[old.ID] = &old

	imgKey := EncodeAccessKey(img.AccessKey)
	docKey := EncodeAccessKey(doc.AccessKey)
	const other = 8 // 不是上传者
	reqs := []DownloadRequest{
		{FileID: img.ID, AccessKey: imgKey, Variant: VariantOriginal},           // 密钥正确
		{FileID: img.ID, AccessKey: imgKey, Variant: VariantThumbnail},          // 缩略图
		{FileID: doc.ID, AccessKey: imgKey, Variant: VariantOriginal},           // 密钥属于别的文件
		{FileID: doc.ID, AccessKey: "not-base64!", Variant: VariantOriginal},    // 格式错误
		{FileID: doc.ID, AccessKey: docKey, Variant: VariantThumbnail},          // 普通文件没有缩略图
		{FileID: avatar.ID, Variant: VariantOriginal},                           // 头像公开
		{FileID: 424242, AccessKey: docKey, Variant: VariantOriginal},           // 不存在
		{FileID: old.ID, AccessKey: EncodeAccessKey(old.AccessKey), Variant: 1}, // 另一种存储
		{FileID: doc.ID, AccessKey: docKey, Variant: VariantOriginal},           // 密钥正确
	}
	urls, err := fx.uc.DownloadURLs(context.Background(), other, reqs)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		id      int64
		variant int32
	}{{img.ID, VariantOriginal}, {img.ID, VariantThumbnail}, {avatar.ID, VariantOriginal}, {doc.ID, VariantOriginal}}
	if len(urls) != len(want) {
		t.Fatalf("got %d urls: %+v", len(urls), urls)
	}
	for i, w := range want {
		if urls[i].FileID != w.id || urls[i].Variant != w.variant {
			t.Fatalf("url %d = %+v, want %+v", i, urls[i], w)
		}
	}
	if !strings.Contains(urls[3].URL, "ct=application/octet-stream") || !strings.Contains(urls[0].URL, "ct=image/png") {
		t.Fatalf("content types not passed to storage: %v / %v", urls[0].URL, urls[3].URL)
	}
	if d := time.Until(urls[0].ExpiresAt); d < 9*time.Minute || d > 10*time.Minute {
		t.Fatalf("expires in %v", d)
	}

	// 上传者不带密钥也可以下载
	urls, err = fx.uc.DownloadURLs(context.Background(), 7, []DownloadRequest{{FileID: doc.ID, Variant: VariantOriginal}})
	if err != nil || len(urls) != 1 {
		t.Fatalf("uploader: %v %v", urls, err)
	}
}

type fakeContentStore struct {
	*memStorage
	validSig string
}

func (s *fakeContentStore) VerifyURL(_ int64, _ int32, _ int64, sig string) bool {
	return sig == s.validSig
}

func (s *fakeContentStore) Open(key string) (Content, error) {
	data, ok := s.objects[key]
	if !ok {
		return nil, ErrNotFound
	}
	return nopContent{bytes.NewReader(data)}, nil
}

type nopContent struct{ *bytes.Reader }

func (nopContent) Close() error       { return nil }
func (nopContent) ModTime() time.Time { return time.Time{} }

func TestOpenContent(t *testing.T) {
	fx := newFixture()
	cs := &fakeContentStore{memStorage: fx.storage, validSig: "good"}
	fx.uc.storage = cs
	doc, err := fx.upload(PurposeChat, "a.txt", []byte("data"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	future := time.Now().Add(time.Minute).Unix()

	c, err := fx.uc.OpenContent(ctx, doc.ID, VariantOriginal, future, "good")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(c)
	if string(got) != "data" || c.ContentType != "application/octet-stream" || !strings.HasPrefix(c.ContentDisposition, "attachment;") {
		t.Fatalf("content %q type %q disposition %q", got, c.ContentType, c.ContentDisposition)
	}

	if _, err := fx.uc.OpenContent(ctx, doc.ID, VariantOriginal, future, "bad"); reason(err) != "DOWNLOAD_URL_INVALID" {
		t.Fatalf("bad signature: %v", err)
	}
	if _, err := fx.uc.OpenContent(ctx, doc.ID, VariantOriginal, time.Now().Add(-time.Second).Unix(), "good"); reason(err) != "DOWNLOAD_URL_INVALID" {
		t.Fatalf("expired: %v", err)
	}
	if _, err := fx.uc.OpenContent(ctx, doc.ID, VariantThumbnail, future, "good"); reason(err) != "FILE_NOT_FOUND" {
		t.Fatalf("missing thumbnail: %v", err)
	}
	if _, err := fx.uc.OpenContent(ctx, 424242, VariantOriginal, future, "good"); reason(err) != "FILE_NOT_FOUND" {
		t.Fatalf("missing record: %v", err)
	}
	delete(fx.storage.objects, ObjectKey(doc.ID, VariantOriginal))
	if _, err := fx.uc.OpenContent(ctx, doc.ID, VariantOriginal, future, "good"); reason(err) != "FILE_NOT_FOUND" {
		t.Fatalf("missing object: %v", err)
	}

	// 当前存储不提供下载内容（OSS）
	fx.uc.storage = fx.storage
	if _, err := fx.uc.OpenContent(ctx, doc.ID, VariantOriginal, future, "good"); reason(err) != "FILE_NOT_FOUND" {
		t.Fatalf("oss mode: %v", err)
	}
}

func TestGetFilesKeepsOrder(t *testing.T) {
	fx := newFixture()
	a, _ := fx.upload(PurposeChat, "a.txt", []byte("a"))
	b, _ := fx.upload(PurposeChat, "b.txt", []byte("b"))
	files, err := fx.uc.GetFiles(context.Background(), []int64{b.ID, 424242, a.ID, b.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].ID != b.ID || files[1].ID != a.ID {
		t.Fatalf("got %+v", files)
	}
}
