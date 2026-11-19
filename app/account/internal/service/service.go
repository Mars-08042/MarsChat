// Package service 把 proto 接口适配到 biz 用例：参数转换、取当前用户与客户端 IP。
// 参数格式由 protovalidate 中间件在进入这里之前校验。
package service

import (
	"context"
	"math"
	"time"

	"github.com/google/wire"
	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "github.com/Mars-08042/MarsChat/api/account/v1"
	"github.com/Mars-08042/MarsChat/app/account/internal/biz"
	"github.com/Mars-08042/MarsChat/app/account/internal/conf"
	"github.com/Mars-08042/MarsChat/pkg/auth"
	"github.com/Mars-08042/MarsChat/pkg/clientip"
)

// ProviderSet 是 service 层的依赖提供者。
var ProviderSet = wire.NewSet(NewClientIPResolver, NewAccountService, NewInternalService)

// NewClientIPResolver 按配置的可信代理解析客户端 IP。
func NewClientIPResolver(c *conf.Security) (*clientip.Resolver, error) {
	return clientip.NewResolver(c.GetTrustedProxies())
}

// principal 取当前用户。鉴权中间件保证需要登录的接口一定有值，取不到说明路由配置有误。
func principal(ctx context.Context) (auth.Principal, error) {
	p, ok := auth.FromContext(ctx)
	if !ok {
		return auth.Principal{}, auth.ErrUnauthorized("missing principal")
	}
	return p, nil
}

func toDevice(d *v1.Device) biz.Device {
	return biz.Device{ID: d.GetDeviceId(), Platform: int32(d.GetPlatform()), Name: d.GetDeviceName()}
}

func toUser(u *biz.User) *v1.User {
	return &v1.User{
		Id:        u.ID,
		Phone:     u.Phone,
		Nickname:  u.Nickname,
		Avatar:    u.Avatar,
		Gender:    v1.Gender(u.Gender),
		Birthday:  formatBirthday(u.Birthday),
		Signature: u.Signature,
		Role:      v1.Role(u.Role),
		Status:    v1.UserStatus(u.Status),
		CreatedAt: timestamppb.New(u.CreatedAt),
	}
}

func toProfile(u *biz.User) *v1.UserProfile {
	return &v1.UserProfile{
		Id:        u.ID,
		Nickname:  u.Nickname,
		Avatar:    u.Avatar,
		Gender:    v1.Gender(u.Gender),
		Signature: u.Signature,
		Status:    v1.UserStatus(u.Status),
	}
}

func formatBirthday(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(time.DateOnly)
}

// toTokenPair 把过期时间换成剩余秒数，客户端不依赖本地时钟与服务端一致。
func toTokenPair(p *biz.TokenPair, now time.Time) *v1.TokenPair {
	return &v1.TokenPair{
		AccessToken:      p.AccessToken,
		AccessExpiresIn:  int32(math.Round(p.AccessExpiresAt.Sub(now).Seconds())),
		RefreshToken:     p.RefreshToken,
		RefreshExpiresIn: int32(math.Round(p.RefreshExpiresAt.Sub(now).Seconds())),
	}
}

func toSession(s *biz.Session, current int64) *v1.Session {
	return &v1.Session{
		Id:           s.ID,
		Platform:     v1.Platform(s.Device.Platform),
		DeviceName:   s.Device.Name,
		LastIp:       s.LastIP,
		CreatedAt:    timestamppb.New(s.CreatedAt),
		LastActiveAt: timestamppb.New(s.LastActiveAt),
		Current:      s.ID == current,
	}
}
