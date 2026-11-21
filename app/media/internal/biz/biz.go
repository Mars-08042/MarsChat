// Package biz 是 media 服务的业务逻辑：上传规则、类型识别、缩略图、下载权限与下载地址。
// 存储细节由 data 层实现这里定义的接口。
package biz

import (
	"context"
	"errors"
	"io"
	"strconv"
	"time"

	"github.com/google/wire"

	"github.com/Mars-08042/MarsChat/pkg/idgen"
	"github.com/Mars-08042/MarsChat/pkg/ratelimit"
)

// ProviderSet 是 biz 层的依赖提供者。
var ProviderSet = wire.NewSet(NewThumbnailer, NewFileUsecase)

// ErrNotFound 由数据层返回，表示记录或对象不存在。
var ErrNotFound = errors.New("not found")

// 取值与 media.v1 中的枚举一致，数据库直接存这些数值。
const (
	PurposeChat   int32 = 1
	PurposeAvatar int32 = 2

	KindFile  int32 = 1
	KindImage int32 = 2

	VariantOriginal  int32 = 1
	VariantThumbnail int32 = 2

	StorageLocal int32 = 1
	StorageOSS   int32 = 2
)

// AccessKeySize 是访问密钥的字节数（128 位）。
const AccessKeySize = 16

// File 是文件记录，写入后不再变化。
type File struct {
	ID            int64
	UploaderID    int64
	Purpose       int32
	Kind          int32
	Name          string
	Size          int64
	MimeType      string
	SHA256        []byte
	Width         int32
	Height        int32
	ThumbnailSize int32
	AccessKey     []byte
	Storage       int32
	CreatedAt     time.Time
}

// HasThumbnail 判断是否有缩略图。
func (f *File) HasThumbnail() bool {
	return f.Kind == KindImage && f.ThumbnailSize > 0
}

// ObjectKey 返回文件某个变体在存储中的键。键只由 ID 推出，不含用户输入，不会出现路径穿越。
func ObjectKey(id int64, variant int32) string {
	if variant == VariantThumbnail {
		return "thumbs/" + strconv.FormatInt(id, 10) + ".jpg"
	}
	return "files/" + strconv.FormatInt(id, 10)
}

// FileRepo 是文件记录的存储。Get 系列经过元数据缓存。
type FileRepo interface {
	Create(ctx context.Context, f *File) error
	// Get 返回文件，不存在时返回 ErrNotFound
	Get(ctx context.Context, id int64) (*File, error)
	// BatchGet 返回存在的文件，键为 ID
	BatchGet(ctx context.Context, ids []int64) (map[int64]*File, error)
}

// Object 描述一次签发下载地址所需的信息。
type Object struct {
	FileID  int64
	Variant int32
	Key     string
	// 下载响应的 Content-Type 与 Content-Disposition，由 ServeHeaders 决定
	ContentType        string
	ContentDisposition string
}

// Storage 是文件存储后端（local 或 oss）。
type Storage interface {
	// Kind 返回 StorageLocal 或 StorageOSS，记录在文件记录中
	Kind() int32
	// Put 写入对象：从 r 读取恰好 size 字节；r 出错时返回错误，不留下不完整的对象
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	// Delete 删除对象，对象不存在时也返回 nil
	Delete(ctx context.Context, key string) error
	// SignURL 生成在 expires 之前有效的下载地址，只做本地计算，不访问网络
	SignURL(ctx context.Context, obj Object, expires time.Time) (string, error)
}

// Content 是本地存储打开的文件内容。
type Content interface {
	io.ReadSeekCloser
	ModTime() time.Time
}

// ContentStore 由需要 media 自己提供下载内容的存储（local）实现。
type ContentStore interface {
	// VerifyURL 校验下载地址的签名，不检查过期时间
	VerifyURL(fileID int64, variant int32, expires int64, signature string) bool
	// Open 打开对象，不存在时返回 ErrNotFound
	Open(key string) (Content, error)
}

// RateLimiter 是按键的滑动窗口限流。
type RateLimiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (ratelimit.Result, error)
}

// IDGenerator 生成全局唯一 ID。
type IDGenerator interface {
	NextID() (int64, error)
}

// nextID 分配 ID。节点号租约失效（Redis 长时间不可达）是可预期、可恢复的依赖故障，返回 503 让客户端稍后重试，
// 不当作内部错误；租约的丢失与恢复由 idgen 自己记日志。
func nextID(ids IDGenerator) (int64, error) {
	id, err := ids.NextID()
	if errors.Is(err, idgen.ErrLeaseLost) {
		return 0, ErrDependencyUnavailable()
	}
	return id, err
}
