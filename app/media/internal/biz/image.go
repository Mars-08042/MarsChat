package biz

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/gif" // 注册解码器，GIF 取第一帧
	"image/jpeg"
	_ "image/png"
	"time"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
	"golang.org/x/sync/semaphore"
)

const (
	// 同一实例同时只解码一张图片。解码内存 = 位图（8 位每像素 4 字节，16 位 PNG 每像素 8 字节）
	// + CatmullRom 缩放的中间缓冲（目标宽 × 原图高 × 32 字节）；1600 万像素的 16 位 PNG 约 128 + 41 MB。
	// 两个并发的最坏情况会超过 GOMEMLIMIT（320 MiB）：2026-11-21 实测两个并发解码 1440 万像素的 8 位 PNG，
	// 进程峰值常驻内存已到 330 MiB。
	thumbnailConcurrency = 1
	// 待解码的图片整体读入内存，所有上传请求合计不超过这个预算，防止并发上传把内存撑爆。
	// 加上一个解码任务和进程基线，最坏约 260 MiB，留在 GOMEMLIMIT 以内。
	imageBufferBudget = 64 << 20
	// 排队超过这个时间返回 503，不让上传请求无限等待
	thumbnailWait    = 5 * time.Second
	thumbnailQuality = 80
)

// errNotImage 表示不是可解码的图片或超过像素上限，调用方按普通文件处理。
var errNotImage = errors.New("not a decodable image")

// ImageInfo 是解码结果。
type ImageInfo struct {
	Width     int
	Height    int
	Thumbnail []byte
}

// Thumbnailer 解码图片并生成 JPEG 缩略图，限制同时进行的解码数和读入内存的图片总字节数。
type Thumbnailer struct {
	sem     chan struct{}
	buffers *semaphore.Weighted
	budget  int64
	wait    time.Duration
}

// NewThumbnailer 创建缩略图生成器。
func NewThumbnailer() *Thumbnailer {
	return newThumbnailer(thumbnailConcurrency, imageBufferBudget, thumbnailWait)
}

func newThumbnailer(concurrency int, budget int64, wait time.Duration) *Thumbnailer {
	return &Thumbnailer{sem: make(chan struct{}, concurrency), buffers: semaphore.NewWeighted(budget), budget: budget, wait: wait}
}

// ReserveBuffer 在把图片读入内存之前预留 n 字节，所有请求合计不超过预算；等待超过 wait 返回 503。
// n 超过预算时按预算计（独占），否则永远等不到。成功时必须调用返回的 release。
func (t *Thumbnailer) ReserveBuffer(ctx context.Context, n int64) (release func(), err error) {
	n = min(n, t.budget)
	wctx, cancel := context.WithTimeout(ctx, t.wait)
	defer cancel()
	if err := t.buffers.Acquire(wctx, n); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrDependencyUnavailable().WithCause(errors.New("image buffer budget exhausted"))
	}
	return func() { t.buffers.Release(n) }, nil
}

// Make 生成最长边不超过 maxSide 的缩略图（原图更小时保持原尺寸），透明背景填白色。不处理 JPEG 的 EXIF 方向（TD-38）。
// 不是可解码的图片或像素数超过 maxPixels 时返回 errNotImage；排队超时返回 503。
// 先用 DecodeConfig 只读文件头检查尺寸，防止小文件解码出超大位图（解压炸弹）。
func (t *Thumbnailer) Make(ctx context.Context, data []byte, maxPixels int64, maxSide int) (*ImageInfo, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errNotImage, err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > maxPixels {
		return nil, fmt.Errorf("%w: %dx%d exceeds pixel limit", errNotImage, cfg.Width, cfg.Height)
	}

	timer := time.NewTimer(t.wait)
	defer timer.Stop()
	select {
	case t.sem <- struct{}{}:
		defer func() { <-t.sem }()
	case <-timer.C:
		return nil, ErrDependencyUnavailable().WithCause(errors.New("thumbnail queue timeout"))
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errNotImage, err)
	}
	w, h := fitSize(cfg.Width, cfg.Height, maxSide)
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), image.White, image.Point{}, draw.Src)
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, img.Bounds(), draw.Over, nil)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: thumbnailQuality}); err != nil {
		return nil, fmt.Errorf("encode thumbnail: %w", err)
	}
	return &ImageInfo{Width: cfg.Width, Height: cfg.Height, Thumbnail: buf.Bytes()}, nil
}

// fitSize 按比例缩放到最长边不超过 maxSide，短边至少 1 像素。
func fitSize(w, h, maxSide int) (int, int) {
	if w <= maxSide && h <= maxSide {
		return w, h
	}
	if w >= h {
		return maxSide, max(1, h*maxSide/w)
	}
	return max(1, w*maxSide/h), maxSide
}
