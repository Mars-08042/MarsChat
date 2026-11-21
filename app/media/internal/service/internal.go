package service

import (
	"context"

	v1 "github.com/Mars-08042/MarsChat/api/media/v1"
	"github.com/Mars-08042/MarsChat/app/media/internal/biz"
)

// InternalService 实现 media.v1.MediaInternalService，只在内网 gRPC 上提供。
type InternalService struct {
	v1.UnimplementedMediaInternalServiceServer

	files *biz.FileUsecase
}

// NewInternalService 创建内部接口。
func NewInternalService(files *biz.FileUsecase) *InternalService {
	return &InternalService{files: files}
}

func (s *InternalService) GetFiles(ctx context.Context, req *v1.GetFilesRequest) (*v1.GetFilesResponse, error) {
	files, err := s.files.GetFiles(ctx, req.GetIds())
	if err != nil {
		return nil, err
	}
	resp := &v1.GetFilesResponse{Files: make([]*v1.FileInfo, len(files))}
	for i, f := range files {
		resp.Files[i] = toFileInfo(f)
	}
	return resp, nil
}
