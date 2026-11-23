package biz

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"strconv"
	"strings"
	"time"

	v1 "github.com/Mars-08042/MarsChat/api/access/v1"
	"github.com/Mars-08042/MarsChat/app/access/internal/conf"
)

// 配置没有写 ttl 时的凭据有效期（docs/design/turn-relay.md 第 5 节）
const defaultTurnTTL = 12 * time.Hour

// turnServers 按 coturn 的 TURN REST API 约定（use-auth-secret）签发临时凭据：
// 用户名为「过期时刻 Unix 秒:用户 ID」，密码为 base64(HMAC-SHA1(共享密钥, 用户名))。
// coturn 用同一个密钥重新计算并检查过期时刻，不需要和 access 同步任何状态。
type turnServers struct {
	urls   []string
	secret []byte
	ttl    time.Duration
	now    func() time.Time
}

// newTurnServers 在没有配置地址时返回 nil，issue 对 nil 返回空列表，客户端只用本机地址。
func newTurnServers(c *conf.Turn) *turnServers {
	var urls []string
	for u := range strings.SplitSeq(c.GetUrls(), ",") {
		if u = strings.TrimSpace(u); u != "" {
			urls = append(urls, u)
		}
	}
	if len(urls) == 0 {
		return nil
	}
	ttl := c.GetTtl().AsDuration()
	if ttl <= 0 {
		ttl = defaultTurnTTL
	}
	return &turnServers{urls: urls, secret: []byte(c.GetSecret()), ttl: ttl, now: time.Now}
}

// issue 为用户签发这通通话用的 ICE 服务器列表。
func (t *turnServers) issue(userID int64) []*v1.IceServer {
	if t == nil {
		return nil
	}
	username := strconv.FormatInt(t.now().Add(t.ttl).Unix(), 10) + ":" + strconv.FormatInt(userID, 10)
	return []*v1.IceServer{{
		Urls:       t.urls,
		Username:   username,
		Credential: turnCredential(t.secret, username),
	}}
}

// turnCredential 与 coturn 校验时的计算相同（默认的 SHA1）。
func turnCredential(secret []byte, username string) string {
	mac := hmac.New(sha1.New, secret)
	mac.Write([]byte(username))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}
