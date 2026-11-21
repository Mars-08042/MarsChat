// Package service 把 proto 接口和自定义的 HTTP 处理函数适配到 biz 用例：参数转换、取当前用户。
// 参数格式由 protovalidate 中间件在进入这里之前校验。
package service

import (
	"context"

	"github.com/google/wire"
	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "github.com/Mars-08042/MarsChat/api/media/v1"
	"github.com/Mars-08042/MarsChat/app/media/internal/biz"
	"github.com/Mars-08042/MarsChat/pkg/auth"
)

// ProviderSet 是 service 层的依赖提供者。
var ProviderSet = wire.NewSet(NewMediaService, NewInternalService)

// principal 取当前用户。鉴权中间件保证需要登录的接口一定有值，取不到说明路由配置有误。
func principal(ctx context.Context) (auth.Principal, error) {
	p, ok := auth.FromContext(ctx)
	if !ok {
		return auth.Principal{}, auth.ErrUnauthorized("missing principal")
	}
	return p, nil
}

func toFileInfo(f *biz.File) *v1.FileInfo {
	return &v1.FileInfo{
		Id:           f.ID,
		AccessKey:    biz.EncodeAccessKey(f.AccessKey),
		Purpose:      v1.Purpose(f.Purpose),
		Kind:         v1.FileKind(f.Kind),
		Name:         f.Name,
		Size:         f.Size,
		MimeType:     f.MimeType,
		Width:        f.Width,
		Height:       f.Height,
		HasThumbnail: f.HasThumbnail(),
		UploaderId:   f.UploaderID,
		CreatedAt:    timestamppb.New(f.CreatedAt),
	}
}
