package data

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"gorm.io/gorm"

	"github.com/Mars-08042/MarsChat/app/media/internal/biz"
	"github.com/Mars-08042/MarsChat/app/media/internal/conf"
	"github.com/Mars-08042/MarsChat/pkg/cache"
)

// filePO 对应 files 表。
type filePO struct {
	ID            int64 `gorm:"primaryKey;autoIncrement:false"`
	UploaderID    int64
	Purpose       int32
	Kind          int32
	Name          string
	Size          int64
	MimeType      string
	SHA256        []byte `gorm:"column:sha256"`
	Width         int32
	Height        int32
	ThumbnailSize int32
	AccessKey     []byte
	Storage       int32
	CreatedAt     time.Time
}

func (filePO) TableName() string { return "files" }

// cachedFile 是缓存中的文件记录。记录写入后不再变化，缓存不需要删除；
// 字段变化时旧缓存按 JSON 规则解码，新增字段为零值，改变字段含义时要换键名。
// []byte 在 JSON 中编码为 base64。
type cachedFile struct {
	ID            int64     `json:"id"`
	UploaderID    int64     `json:"uploader_id"`
	Purpose       int32     `json:"purpose"`
	Kind          int32     `json:"kind"`
	Name          string    `json:"name"`
	Size          int64     `json:"size"`
	MimeType      string    `json:"mime_type"`
	SHA256        []byte    `json:"sha256"`
	Width         int32     `json:"width,omitempty"`
	Height        int32     `json:"height,omitempty"`
	ThumbnailSize int32     `json:"thumbnail_size,omitempty"`
	AccessKey     []byte    `json:"access_key"`
	Storage       int32     `json:"storage"`
	CreatedAt     time.Time `json:"created_at"`
}

func newCachedFile(p *filePO) cachedFile {
	return cachedFile{
		ID: p.ID, UploaderID: p.UploaderID, Purpose: p.Purpose, Kind: p.Kind, Name: p.Name, Size: p.Size,
		MimeType: p.MimeType, SHA256: p.SHA256, Width: p.Width, Height: p.Height, ThumbnailSize: p.ThumbnailSize,
		AccessKey: p.AccessKey, Storage: p.Storage, CreatedAt: p.CreatedAt,
	}
}

func (c cachedFile) toBiz() *biz.File {
	return &biz.File{
		ID: c.ID, UploaderID: c.UploaderID, Purpose: c.Purpose, Kind: c.Kind, Name: c.Name, Size: c.Size,
		MimeType: c.MimeType, SHA256: c.SHA256, Width: c.Width, Height: c.Height, ThumbnailSize: c.ThumbnailSize,
		AccessKey: c.AccessKey, Storage: c.Storage, CreatedAt: c.CreatedAt,
	}
}

func fileCacheKey(id int64) string {
	return "media:file:" + strconv.FormatInt(id, 10)
}

type fileRepo struct {
	d     *Data
	cache *cache.Cache[cachedFile]
}

// NewFileRepo 创建文件记录存储。文件 ID 不对外列举，空值标记足以防穿透，不加布隆过滤器。
func NewFileRepo(d *Data, c *conf.Cache, logger *slog.Logger) biz.FileRepo {
	return &fileRepo{
		d: d,
		cache: cache.New[cachedFile](d.rdb, cache.Options{
			Name:    "media_file",
			TTL:     c.GetFileTtl().AsDuration(),
			NullTTL: c.GetNullTtl().AsDuration(),
		}, logger),
	}
}

func (r *fileRepo) Create(ctx context.Context, f *biz.File) error {
	po := &filePO{
		ID: f.ID, UploaderID: f.UploaderID, Purpose: f.Purpose, Kind: f.Kind, Name: f.Name, Size: f.Size,
		MimeType: f.MimeType, SHA256: f.SHA256, Width: f.Width, Height: f.Height, ThumbnailSize: f.ThumbnailSize,
		AccessKey: f.AccessKey, Storage: f.Storage, CreatedAt: f.CreatedAt,
	}
	return r.d.db.WithContext(ctx).Create(po).Error
}

func (r *fileRepo) Get(ctx context.Context, id int64) (*biz.File, error) {
	v, err := r.cache.Get(ctx, fileCacheKey(id), func(ctx context.Context) (cachedFile, error) {
		var po filePO
		err := r.d.db.WithContext(ctx).Where("id = ?", id).Take(&po).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return cachedFile{}, cache.ErrNotFound
		}
		if err != nil {
			return cachedFile{}, err
		}
		return newCachedFile(&po), nil
	})
	if errors.Is(err, cache.ErrNotFound) {
		return nil, biz.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return v.toBiz(), nil
}

func (r *fileRepo) BatchGet(ctx context.Context, ids []int64) (map[int64]*biz.File, error) {
	keys := make([]string, len(ids))
	byKey := make(map[string]int64, len(ids))
	for i, id := range ids {
		keys[i] = fileCacheKey(id)
		byKey[keys[i]] = id
	}
	got, err := r.cache.GetMany(ctx, keys, func(ctx context.Context, missing []string) (map[string]cachedFile, error) {
		missingIDs := make([]int64, len(missing))
		for i, k := range missing {
			missingIDs[i] = byKey[k]
		}
		var pos []filePO
		if err := r.d.db.WithContext(ctx).Where("id IN ?", missingIDs).Find(&pos).Error; err != nil {
			return nil, err
		}
		out := make(map[string]cachedFile, len(pos))
		for i := range pos {
			out[fileCacheKey(pos[i].ID)] = newCachedFile(&pos[i])
		}
		return out, nil
	})
	if err != nil {
		return nil, err
	}
	out := make(map[int64]*biz.File, len(got))
	for k, v := range got {
		out[byKey[k]] = v.toBiz()
	}
	return out, nil
}
