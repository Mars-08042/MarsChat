package service

import (
	"context"
	"net/http"
	"strconv"
	"time"

	khttp "github.com/go-kratos/kratos/v3/transport/http"
	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "github.com/Mars-08042/MarsChat/api/media/v1"
	"github.com/Mars-08042/MarsChat/app/media/internal/biz"
)

// 自定义 HTTP 处理函数的操作名，用于访问日志和鉴权白名单，命名与生成代码一致。
const (
	OperationUploadFile      = "/media.v1.MediaService/UploadFile"
	OperationDownloadContent = "/media.v1.MediaService/DownloadContent"
)

// MediaService 实现 media.v1.MediaService，以及上传、本地下载两个自定义 HTTP 处理函数。
type MediaService struct {
	v1.UnimplementedMediaServiceServer

	files *biz.FileUsecase
}

// NewMediaService 创建对外接口。
func NewMediaService(files *biz.FileUsecase) *MediaService {
	return &MediaService{files: files}
}

func (s *MediaService) GetDownloadUrls(ctx context.Context, req *v1.GetDownloadUrlsRequest) (*v1.GetDownloadUrlsResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]biz.DownloadRequest, len(req.GetItems()))
	for i, it := range req.GetItems() {
		items[i] = biz.DownloadRequest{FileID: it.GetFileId(), AccessKey: it.GetAccessKey(), Variant: int32(it.GetVariant())}
	}
	urls, err := s.files.DownloadURLs(ctx, p.UserID, items)
	if err != nil {
		return nil, err
	}
	resp := &v1.GetDownloadUrlsResponse{Urls: make([]*v1.DownloadUrl, len(urls))}
	for i, u := range urls {
		resp.Urls[i] = &v1.DownloadUrl{
			FileId: u.FileID, Variant: v1.Variant(u.Variant), Url: u.URL, ExpiresAt: timestamppb.New(u.ExpiresAt),
		}
	}
	return resp, nil
}

// UploadFile 处理 POST /v1/files?purpose=&name=，请求体是文件的原始字节。
// 与生成代码一样先设置操作名、绑定参数，再经过服务器的中间件链（访问日志、鉴权、参数校验）。
func (s *MediaService) UploadFile(ctx khttp.Context) error {
	khttp.SetOperation(ctx, OperationUploadFile)
	var in v1.UploadFileRequest
	if err := ctx.BindQuery(&in); err != nil {
		return err
	}
	req := ctx.Request()
	// 读取请求体不感知 ctx：把服务器超时同时设为读取截止时间，慢速或停滞的上传到点后读取失败，不会一直占着连接
	if deadline, ok := ctx.Deadline(); ok {
		_ = http.NewResponseController(ctx.Response()).SetReadDeadline(deadline)
	}
	h := ctx.Middleware(func(c context.Context, r any) (any, error) {
		in := r.(*v1.UploadFileRequest)
		p, err := principal(c)
		if err != nil {
			return nil, err
		}
		f, err := s.files.Upload(c, biz.UploadInput{
			UploaderID: p.UserID,
			Purpose:    int32(in.GetPurpose()),
			Name:       in.GetName(),
			Size:       req.ContentLength,
			Body:       req.Body,
		})
		if err != nil {
			return nil, err
		}
		return toFileInfo(f), nil
	})
	out, err := h(ctx, &in)
	if err != nil {
		return err
	}
	return ctx.Result(http.StatusOK, out)
}

// DownloadContent 处理本地存储的下载地址 GET /v1/files/{file_id}/content?variant=&expires=&signature=。
// 签名即授权，不需要访问令牌（在鉴权白名单中）；支持 Range 与条件请求。
func (s *MediaService) DownloadContent(ctx khttp.Context) error {
	khttp.SetOperation(ctx, OperationDownloadContent)
	var in v1.DownloadContentRequest
	if err := ctx.BindQuery(&in); err != nil {
		return err
	}
	if err := ctx.BindVars(&in); err != nil {
		return err
	}
	h := ctx.Middleware(func(c context.Context, r any) (any, error) {
		in := r.(*v1.DownloadContentRequest)
		content, err := s.files.OpenContent(c, in.GetFileId(), int32(in.GetVariant()), in.GetExpires(), in.GetSignature())
		if err != nil {
			return nil, err
		}
		defer content.Close()
		hdr := ctx.Response().Header()
		hdr.Set("Content-Type", content.ContentType)
		hdr.Set("Content-Disposition", content.ContentDisposition)
		// 非图片按 application/octet-stream 返回，再禁止浏览器嗅探类型，防止把上传的 HTML 当页面执行
		hdr.Set("X-Content-Type-Options", "nosniff")
		// 内容不会变化：浏览器可以在地址有效期内缓存，不与其他用户共享
		if ttl := time.Until(time.Unix(in.GetExpires(), 0)); ttl > 0 {
			hdr.Set("Cache-Control", "private, max-age="+strconv.Itoa(int(ttl.Seconds())))
		}
		http.ServeContent(ctx.Response(), ctx.Request(), "", content.ModTime(), content)
		return nil, nil
	})
	_, err := h(ctx, &in)
	return err
}
