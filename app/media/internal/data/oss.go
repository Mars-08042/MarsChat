package data

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss/credentials"

	"github.com/Mars-08042/MarsChat/app/media/internal/biz"
	"github.com/Mars-08042/MarsChat/app/media/internal/conf"
	"github.com/Mars-08042/MarsChat/pkg/idgen"
)

// 建立连接的超时。单次读写的超时用 SDK 默认值：上传是长时间的流式写入，总时长由请求的截止时间控制
const ossConnectTimeout = 5 * time.Second

// ossStorage 把文件写到阿里云 OSS（Bucket 保持私有），下载用 V4 预签名地址，不经过 media。
type ossStorage struct {
	// client 用于上传和删除，可以走内网地址
	client *oss.Client
	// presigner 用于生成下载地址：配置了自定义域名时使用 CNAME 方式，否则与 client 相同
	presigner *oss.Client
	bucket    string
	prefix    string
}

func newOSSStorage(c *conf.Oss) (*ossStorage, error) {
	var missing []string
	for _, f := range []struct{ name, value string }{
		{"region", c.GetRegion()}, {"bucket", c.GetBucket()},
		{"access_key_id", c.GetAccessKeyId()}, {"access_key_secret", c.GetAccessKeySecret()},
	} {
		if f.value == "" {
			missing = append(missing, f.name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("storage.oss is incomplete, missing %v", missing)
	}
	creds := credentials.NewStaticCredentialsProvider(c.GetAccessKeyId(), c.GetAccessKeySecret())
	newConfig := func() *oss.Config {
		return oss.LoadDefaultConfig().
			WithRegion(c.GetRegion()).
			WithCredentialsProvider(creds).
			WithSignatureVersion(oss.SignatureVersionV4).
			WithConnectTimeout(ossConnectTimeout)
	}

	cfg := newConfig()
	if ep := c.GetEndpoint(); ep != "" {
		cfg = cfg.WithEndpoint(ep)
	}
	s := &ossStorage{client: oss.NewClient(cfg), bucket: c.GetBucket(), prefix: c.GetKeyPrefix()}
	s.presigner = s.client
	if pub := c.GetPublicEndpoint(); pub != "" {
		s.presigner = oss.NewClient(newConfig().WithEndpoint(pub).WithUseCName(true))
	}
	return s, nil
}

func (*ossStorage) Kind() int32 { return biz.StorageOSS }

// objectName 把对象键（biz.ObjectKey：files/{id}、thumbs/{id}.jpg）映射为 OSS 中的对象名
// {key_prefix}files/2026/11/{id}，在控制台中按年、月分目录浏览。年月取自 snowflake ID 中的生成时间（UTC），
// 对象名仍然只由 ID 推出，数据库不用另存路径；月初 0～8 点（北京时间）上传的文件归入上个月的目录。
func (s *ossStorage) objectName(key string) (string, error) {
	dir, name := path.Split(key)
	idPart, _, _ := strings.Cut(name, ".")
	id, err := strconv.ParseInt(idPart, 10, 64)
	if dir == "" || err != nil || id <= 0 {
		return "", fmt.Errorf("invalid object key %q", key)
	}
	t, _, _ := idgen.Decompose(id)
	return s.prefix + dir + t.Format("2006/01/") + name, nil
}

// Put 流式上传：body 不可 Seek 时 SDK 不会重试，失败直接返回，由客户端重新上传。
func (s *ossStorage) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	name, err := s.objectName(key)
	if err != nil {
		return err
	}
	_, err = s.client.PutObject(ctx, &oss.PutObjectRequest{
		Bucket:        oss.Ptr(s.bucket),
		Key:           oss.Ptr(name),
		Body:          r,
		ContentLength: oss.Ptr(size),
		ContentType:   oss.Ptr(contentType),
	})
	return err
}

// Delete 删除对象。OSS 对不存在的对象同样返回成功。
func (s *ossStorage) Delete(ctx context.Context, key string) error {
	name, err := s.objectName(key)
	if err != nil {
		return err
	}
	_, err = s.client.DeleteObject(ctx, &oss.DeleteObjectRequest{
		Bucket: oss.Ptr(s.bucket),
		Key:    oss.Ptr(name),
	})
	return err
}

// SignURL 生成预签名 GET 地址，只做本地计算。response-content-type 与 response-content-disposition
// 参与签名，客户端不能改成内联显示任意文件。
func (s *ossStorage) SignURL(ctx context.Context, obj biz.Object, expires time.Time) (string, error) {
	name, err := s.objectName(obj.Key)
	if err != nil {
		return "", err
	}
	// 不带 response-content-type：OSS 出于安全考虑拒绝 GET 请求覆盖 Content-Type
	// （400 InvalidRequest，EC 0017-00000902）。响应类型取自 Put 时写入的对象元数据，
	// 两者都来自 biz.ServeHeaders，取值相同。
	res, err := s.presigner.Presign(ctx, &oss.GetObjectRequest{
		Bucket:                     oss.Ptr(s.bucket),
		Key:                        oss.Ptr(name),
		ResponseContentDisposition: oss.Ptr(obj.ContentDisposition),
	}, oss.PresignExpiration(expires))
	if err != nil {
		return "", fmt.Errorf("presign %s: %w", obj.Key, err)
	}
	if res == nil || res.URL == "" {
		return "", errors.New("presign returned empty url")
	}
	return res.URL, nil
}
