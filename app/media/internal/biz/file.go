package biz

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"hash"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

// 删除孤儿对象（写入存储后插入记录失败）的超时，不受已结束请求的取消影响
const cleanupTimeout = 5 * time.Second

// FileUsecase 是文件的上传与下载。
type FileUsecase struct {
	repo    FileRepo
	storage Storage
	thumbs  *Thumbnailer
	ids     IDGenerator
	limiter RateLimiter
	policy  *PolicyHolder
	logger  *slog.Logger
}

// NewFileUsecase 创建文件用例。
func NewFileUsecase(repo FileRepo, storage Storage, thumbs *Thumbnailer, ids IDGenerator, limiter RateLimiter,
	policy *PolicyHolder, logger *slog.Logger) *FileUsecase {
	return &FileUsecase{repo: repo, storage: storage, thumbs: thumbs, ids: ids, limiter: limiter, policy: policy, logger: logger}
}

// UploadInput 是一次上传。
type UploadInput struct {
	UploaderID int64
	Purpose    int32
	Name       string
	// Size 是请求的 Content-Length，未知时为 -1
	Size int64
	Body io.Reader
}

// Upload 保存文件并返回记录。大小在读取请求体之前就检查；类型按文件内容识别，不看扩展名和客户端声明；
// 不超过图片处理上限的光栅图片整体读入内存（所有请求合计受 imageBufferBudget 限制），解码成功则生成缩略图，
// 失败则按普通文件保存；其他文件边读边写入存储。
func (uc *FileUsecase) Upload(ctx context.Context, in UploadInput) (*File, error) {
	p := uc.policy.Load()
	if in.Size < 0 {
		return nil, ErrLengthRequired()
	}
	if in.Size == 0 {
		return nil, ErrInvalidArgument("文件不能为空")
	}
	if limit := maxBytes(p, in.Purpose); in.Size > limit {
		return nil, ErrFileTooLarge(limit)
	}
	if err := uc.checkRate(ctx, in.UploaderID); err != nil {
		return nil, err
	}
	id, err := nextID(uc.ids)
	if err != nil {
		return nil, err
	}

	body := newBodyReader(in.Body, in.Size)
	head := make([]byte, min(512, in.Size))
	if _, err := io.ReadFull(body, head); err != nil {
		return nil, ErrUploadIncomplete().WithCause(err)
	}
	f := &File{
		ID:         id,
		UploaderID: in.UploaderID,
		Purpose:    in.Purpose,
		Kind:       KindFile,
		Name:       SanitizeName(in.Name),
		Size:       in.Size,
		MimeType:   http.DetectContentType(head),
		Storage:    uc.storage.Kind(),
		CreatedAt:  nowUTC(),
	}
	if in.Purpose == PurposeAvatar && !isRasterImage(f.MimeType) {
		return nil, ErrUnsupportedFileType()
	}

	var data []byte
	var img *ImageInfo
	if isRasterImage(f.MimeType) && in.Size <= p.GetMaxImageBytes() {
		release, err := uc.thumbs.ReserveBuffer(ctx, in.Size)
		if err != nil {
			return nil, err
		}
		defer release()
		data = make([]byte, in.Size)
		copy(data, head)
		if _, err := io.ReadFull(body, data[len(head):]); err != nil {
			return nil, ErrUploadIncomplete().WithCause(err)
		}
		img, err = uc.thumbs.Make(ctx, data, p.GetMaxImagePixels(), int(p.GetThumbnailMaxSide()))
		switch {
		case errors.Is(err, errNotImage):
			uc.logger.Debug("image not decodable, storing as plain file", slog.Int64("file_id", id), slog.Any("error", err))
			img = nil
		case err != nil:
			return nil, err
		}
	}
	if in.Purpose == PurposeAvatar && img == nil {
		return nil, ErrUnsupportedFileType()
	}
	if img != nil {
		f.Kind = KindImage
		f.Width, f.Height = int32(img.Width), int32(img.Height)
		f.ThumbnailSize = int32(len(img.Thumbnail))
	}

	contentType, _ := ServeHeaders(f, VariantOriginal)
	key := ObjectKey(id, VariantOriginal)
	var src io.Reader = bytes.NewReader(data)
	if data == nil {
		src = io.MultiReader(bytes.NewReader(head), body)
	}
	if err := uc.storage.Put(ctx, key, src, in.Size, contentType); err != nil {
		if body.err != nil {
			return nil, ErrUploadIncomplete().WithCause(body.err)
		}
		return nil, ErrDependencyUnavailable().WithCause(err)
	}
	written := []string{key}
	if img != nil {
		thumbKey := ObjectKey(id, VariantThumbnail)
		if err := uc.storage.Put(ctx, thumbKey, bytes.NewReader(img.Thumbnail), int64(len(img.Thumbnail)), "image/jpeg"); err != nil {
			uc.cleanup(ctx, written)
			return nil, ErrDependencyUnavailable().WithCause(err)
		}
		written = append(written, thumbKey)
	}

	// 请求体经 bodyReader 读完，摘要覆盖全部字节
	f.SHA256 = body.h.Sum(nil)
	f.AccessKey = make([]byte, AccessKeySize)
	_, _ = rand.Read(f.AccessKey) // crypto/rand.Read 不会返回错误
	if err := uc.repo.Create(ctx, f); err != nil {
		uc.cleanup(ctx, written)
		return nil, err
	}
	return f, nil
}

// cleanup 尽力删除已写入的对象。进程在写入与删除之间退出仍会留下孤儿对象，只占空间，不影响正确性（TD-37）。
func (uc *FileUsecase) cleanup(ctx context.Context, keys []string) {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	for _, k := range keys {
		if err := uc.storage.Delete(cctx, k); err != nil {
			uc.logger.Warn("delete orphan object failed", slog.String("key", k), slog.Any("error", err))
		}
	}
}

// checkRate 按用户限流。Redis 故障时放行：限额用于防滥用，不值得为它让上传整体不可用。
func (uc *FileUsecase) checkRate(ctx context.Context, userID int64) error {
	l := uc.policy.Load().GetUploadLimit()
	key := "media:rl:upload:" + strconv.FormatInt(userID, 10)
	res, err := uc.limiter.Allow(ctx, key, int(l.GetLimit()), l.GetWindow().AsDuration())
	if err != nil {
		uc.logger.Warn("rate limiter unavailable", slog.String("limit", "upload"), slog.Any("error", err))
		return nil
	}
	if !res.Allowed {
		return ErrRateLimited(res.RetryAfter)
	}
	return nil
}

// bodyReader 读取恰好 size 字节的请求体：记录读取错误（据此区分客户端中断与存储故障），
// 不足 size 字节就结束时报 io.ErrUnexpectedEOF，同时计算 SHA-256。
type bodyReader struct {
	r    io.Reader
	h    hash.Hash
	n    int64
	size int64
	err  error
}

func newBodyReader(r io.Reader, size int64) *bodyReader {
	return &bodyReader{r: io.LimitReader(r, size), h: sha256.New(), size: size}
}

func (b *bodyReader) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	b.n += int64(n)
	b.h.Write(p[:n])
	if errors.Is(err, io.EOF) && b.n < b.size {
		err = io.ErrUnexpectedEOF
	}
	if err != nil && !errors.Is(err, io.EOF) {
		b.err = err
	}
	return n, err
}

// DownloadRequest 是一项下载地址请求。
type DownloadRequest struct {
	FileID    int64
	AccessKey string
	Variant   int32
}

// DownloadURL 是签发的下载地址。
type DownloadURL struct {
	FileID    int64
	Variant   int32
	URL       string
	ExpiresAt time.Time
}

// DownloadURLs 为有权访问的项签发下载地址，按请求顺序返回。文件不存在、无权访问、没有对应变体、
// 或写在切换前的另一种存储中的项直接省略，不区分原因，不让客户端借此探测文件 ID。
func (uc *FileUsecase) DownloadURLs(ctx context.Context, userID int64, reqs []DownloadRequest) ([]DownloadURL, error) {
	ids := make([]int64, 0, len(reqs))
	for _, r := range reqs {
		ids = append(ids, r.FileID)
	}
	files, err := uc.repo.BatchGet(ctx, uniqueIDs(ids))
	if err != nil {
		return nil, err
	}
	// 取整秒：本地存储的签名按 Unix 秒计算
	expires := time.Now().Add(uc.policy.Load().GetDownloadUrlTtl().AsDuration()).Truncate(time.Second)
	out := make([]DownloadURL, 0, len(reqs))
	for _, r := range reqs {
		f := files[r.FileID]
		if f == nil || f.Storage != uc.storage.Kind() || !canDownload(f, userID, r.AccessKey) {
			continue
		}
		if r.Variant == VariantThumbnail && !f.HasThumbnail() {
			continue
		}
		contentType, disposition := ServeHeaders(f, r.Variant)
		url, err := uc.storage.SignURL(ctx, Object{
			FileID: f.ID, Variant: r.Variant, Key: ObjectKey(f.ID, r.Variant),
			ContentType: contentType, ContentDisposition: disposition,
		}, expires)
		if err != nil {
			return nil, err
		}
		out = append(out, DownloadURL{FileID: f.ID, Variant: r.Variant, URL: url, ExpiresAt: expires})
	}
	return out, nil
}

// canDownload：头像对所有登录用户公开；聊天文件需要访问密钥匹配（常量时间比较），或者请求者就是上传者。
func canDownload(f *File, userID int64, accessKey string) bool {
	if f.Purpose == PurposeAvatar || f.UploaderID == userID {
		return true
	}
	key, ok := DecodeAccessKey(accessKey)
	return ok && subtle.ConstantTimeCompare(key, f.AccessKey) == 1
}

// EncodeAccessKey 把访问密钥编码为 22 个字符的 base64url（无填充）。
func EncodeAccessKey(key []byte) string {
	return base64.RawURLEncoding.EncodeToString(key)
}

// DecodeAccessKey 解码访问密钥，格式或长度不对时返回 false。
func DecodeAccessKey(s string) ([]byte, bool) {
	key, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(key) != AccessKeySize {
		return nil, false
	}
	return key, true
}

// DownloadContent 是本地存储下载接口要返回的内容。
type DownloadContent struct {
	Content
	ContentType        string
	ContentDisposition string
}

// OpenContent 校验本地存储的下载地址并打开内容。签名错误或已过期返回 403；
// 当前存储不是 local（例如切换到 OSS 后访问旧地址）或对象不存在时返回 404。
func (uc *FileUsecase) OpenContent(ctx context.Context, fileID int64, variant int32, expires int64, signature string) (*DownloadContent, error) {
	cs, ok := uc.storage.(ContentStore)
	if !ok {
		return nil, ErrFileNotFound()
	}
	if !cs.VerifyURL(fileID, variant, expires, signature) || time.Now().Unix() > expires {
		return nil, ErrDownloadURLInvalid()
	}
	f, err := uc.repo.Get(ctx, fileID)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrFileNotFound()
	}
	if err != nil {
		return nil, err
	}
	if variant == VariantThumbnail && !f.HasThumbnail() {
		return nil, ErrFileNotFound()
	}
	c, err := cs.Open(ObjectKey(fileID, variant))
	if errors.Is(err, ErrNotFound) {
		return nil, ErrFileNotFound()
	}
	if err != nil {
		return nil, ErrDependencyUnavailable().WithCause(err)
	}
	contentType, disposition := ServeHeaders(f, variant)
	return &DownloadContent{Content: c, ContentType: contentType, ContentDisposition: disposition}, nil
}

// GetFiles 按 ID 批量返回存在的文件，顺序与输入一致，供其他服务核实文件。
func (uc *FileUsecase) GetFiles(ctx context.Context, ids []int64) ([]*File, error) {
	ids = uniqueIDs(ids)
	m, err := uc.repo.BatchGet(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]*File, 0, len(m))
	for _, id := range ids {
		if f, ok := m[id]; ok {
			out = append(out, f)
		}
	}
	return out, nil
}

func uniqueIDs(ids []int64) []int64 {
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
