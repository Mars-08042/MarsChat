package data

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Mars-08042/MarsChat/app/media/internal/biz"
	"github.com/Mars-08042/MarsChat/app/media/internal/conf"
)

// 签名密钥的最小长度（字符数）。make keys 生成 32 字节随机数的 base64，44 个字符
const minURLSecretLen = 32

// localStorage 把文件写在本地目录，由 media 自己签发下载地址并提供下载（见 biz.ContentStore）。
// 多个实例必须共享同一个目录和签名密钥。
type localStorage struct {
	dir     string
	baseURL string
	secret  []byte
}

func newLocalStorage(c *conf.Local) (*localStorage, error) {
	if c.GetDir() == "" {
		return nil, errors.New("storage.local.dir is required")
	}
	if len(c.GetUrlSecret()) < minURLSecretLen {
		return nil, fmt.Errorf("storage.local.url_secret must be at least %d characters (run make keys)", minURLSecretLen)
	}
	u, err := url.Parse(c.GetPublicBaseUrl())
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" {
		return nil, fmt.Errorf("storage.local.public_base_url must be an absolute http(s) URL without query, got %q", c.GetPublicBaseUrl())
	}
	dir, err := filepath.Abs(c.GetDir())
	if err != nil {
		return nil, fmt.Errorf("resolve storage.local.dir: %w", err)
	}
	s := &localStorage{dir: dir, baseURL: strings.TrimSuffix(u.String(), "/"), secret: []byte(c.GetUrlSecret())}
	if err := s.checkWritable(); err != nil {
		return nil, err
	}
	return s, nil
}

// checkWritable 创建目录并试写一个临时文件，启动时就发现权限问题（例如容器以非 root 用户运行、
// 挂载的宿主机目录不可写），而不是等到第一次上传。
func (s *localStorage) checkWritable() error {
	if err := os.MkdirAll(s.tmpDir(), 0o755); err != nil {
		return fmt.Errorf("create storage dir: %w", err)
	}
	f, err := os.CreateTemp(s.tmpDir(), "probe-*")
	if err != nil {
		return fmt.Errorf("storage dir %s is not writable: %w", s.dir, err)
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}

func (s *localStorage) tmpDir() string { return filepath.Join(s.dir, "tmp") }

func (*localStorage) Kind() int32 { return biz.StorageLocal }

// path 把对象键映射为文件路径。按文件名的哈希分到 256 个子目录（files/3f/{id}），避免单个目录文件过多；
// 不用 ID 的低位分桶：低流量时 snowflake 的序号位多为 0，会集中到同一个目录。
func (s *localStorage) path(key string) (string, error) {
	if key == "" || path.IsAbs(key) || strings.Contains(key, "..") || strings.Contains(key, `\`) {
		return "", fmt.Errorf("invalid object key %q", key)
	}
	dir, name := path.Split(key)
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	shard := fmt.Sprintf("%02x", h.Sum32()&0xff)
	return filepath.Join(s.dir, filepath.FromSlash(dir), shard, name), nil
}

// Put 先写同一文件系统中的临时文件，fsync 后 rename 到目标路径：读者不会看到写了一半的文件，失败时不留下残片。
func (s *localStorage) Put(ctx context.Context, key string, r io.Reader, size int64, _ string) (err error) {
	target, err := s.path(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create dir for %s: %w", key, err)
	}
	tmp, err := os.CreateTemp(s.tmpDir(), "upload-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	n, err := io.Copy(tmp, ctxReader{ctx: ctx, r: r})
	if err != nil {
		return fmt.Errorf("write %s: %w", key, err)
	}
	if n != size {
		return fmt.Errorf("write %s: got %d bytes, want %d", key, n, size)
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", key, err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", key, err)
	}
	if err = os.Rename(tmp.Name(), target); err != nil {
		return fmt.Errorf("rename %s: %w", key, err)
	}
	return nil
}

func (s *localStorage) Delete(_ context.Context, key string) error {
	p, err := s.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// SignURL 生成 media 下载接口的地址：{public_base_url}/v1/files/{id}/content?variant=&expires=&signature=。
// 响应头由下载接口按文件记录重新计算，不放进地址。
func (s *localStorage) SignURL(_ context.Context, obj biz.Object, expires time.Time) (string, error) {
	exp := expires.Unix()
	q := url.Values{}
	q.Set("variant", strconv.Itoa(int(obj.Variant)))
	q.Set("expires", strconv.FormatInt(exp, 10))
	q.Set("signature", s.sign(obj.FileID, obj.Variant, exp))
	return s.baseURL + "/v1/files/" + strconv.FormatInt(obj.FileID, 10) + "/content?" + q.Encode(), nil
}

// sign 对“文件 ID、变体、过期时间”计算 HMAC-SHA256。前缀 v1 便于将来更换签名内容时区分。
func (s *localStorage) sign(fileID int64, variant int32, expires int64) string {
	m := hmac.New(sha256.New, s.secret)
	_, _ = fmt.Fprintf(m, "v1.%d.%d.%d", fileID, variant, expires)
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// VerifyURL 用常量时间比较校验签名，过期时间由调用方检查。
func (s *localStorage) VerifyURL(fileID int64, variant int32, expires int64, signature string) bool {
	return hmac.Equal([]byte(s.sign(fileID, variant, expires)), []byte(signature))
}

func (s *localStorage) Open(key string) (biz.Content, error) {
	p, err := s.path(key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, biz.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return &localContent{File: f, modTime: st.ModTime()}, nil
}

type localContent struct {
	*os.File
	modTime time.Time
}

func (c *localContent) ModTime() time.Time { return c.modTime }

// ctxReader 在每次读取前检查 ctx：写本地文件不感知取消，请求超时或客户端断开后要尽快停止。
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
