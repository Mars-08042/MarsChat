package biz

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math/rand/v2"
	"net/http"
	"testing"
	"time"

	kerrors "github.com/go-kratos/kratos/v3/errors"
)

// 40x30 的半透明无损 WebP，由 Pillow 生成（标准库和 x/image 都没有 WebP 编码器）
const webpFixture = "UklGRjQAAABXRUJQVlA4TCgAAAAvJ0AHELkyRPQ/dhHR/zAQadsU3r/s4aMxGEABQZHpnk8AV9UBqv8C"

func testImage(w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			img.Set(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: 200, A: 255})
		}
	}
	return img
}

func encodePNG(t testing.TB, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, testImage(w, h)); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// noisePNG 生成随机像素的 PNG，压缩后仍接近原始大小，用来构造大文件。
func noisePNG(t testing.TB, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	rng := rand.New(rand.NewPCG(1, 2))
	for i := range img.Pix {
		img.Pix[i] = uint8(rng.UintN(256))
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func encodeJPEG(t testing.TB, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, testImage(w, h), nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func encodeGIF(t testing.TB, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := gif.Encode(&buf, testImage(w, h), nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestThumbnailFormats(t *testing.T) {
	webp, err := base64.StdEncoding.DecodeString(webpFixture)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name         string
		data         []byte
		mime         string
		w, h         int
		thumbW, thmH int
	}{
		{"png landscape", encodePNG(t, 800, 400), "image/png", 800, 400, 320, 160},
		{"jpeg portrait", encodeJPEG(t, 300, 900), "image/jpeg", 300, 900, 106, 320},
		{"gif small keeps size", encodeGIF(t, 50, 40), "image/gif", 50, 40, 50, 40},
		{"webp", webp, "image/webp", 40, 30, 40, 30},
	}
	th := NewThumbnailer()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 与上传时的识别方式一致
			if got := http.DetectContentType(tc.data); got != tc.mime {
				t.Fatalf("DetectContentType = %q, want %q", got, tc.mime)
			}
			info, err := th.Make(context.Background(), tc.data, 16_000_000, 320)
			if err != nil {
				t.Fatal(err)
			}
			if info.Width != tc.w || info.Height != tc.h {
				t.Fatalf("size = %dx%d, want %dx%d", info.Width, info.Height, tc.w, tc.h)
			}
			thumb, format, err := image.Decode(bytes.NewReader(info.Thumbnail))
			if err != nil || format != "jpeg" {
				t.Fatalf("thumbnail decode: format %q, err %v", format, err)
			}
			if b := thumb.Bounds(); b.Dx() != tc.thumbW || b.Dy() != tc.thmH {
				t.Fatalf("thumbnail = %dx%d, want %dx%d", b.Dx(), b.Dy(), tc.thumbW, tc.thmH)
			}
		})
	}
}

func TestThumbnailRejects(t *testing.T) {
	th := NewThumbnailer()
	t.Run("corrupt png", func(t *testing.T) {
		data := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0xAB}, 100)...)
		if _, err := th.Make(context.Background(), data, 16_000_000, 320); !errors.Is(err, errNotImage) {
			t.Fatalf("err = %v, want errNotImage", err)
		}
	})
	t.Run("too many pixels", func(t *testing.T) {
		if _, err := th.Make(context.Background(), encodePNG(t, 20, 20), 399, 320); !errors.Is(err, errNotImage) {
			t.Fatalf("err = %v, want errNotImage", err)
		}
	})
}

func TestThumbnailQueueTimeout(t *testing.T) {
	th := newThumbnailer(1, 1<<20, 20*time.Millisecond)
	th.sem <- struct{}{} // 占满并发
	_, err := th.Make(context.Background(), encodePNG(t, 10, 10), 16_000_000, 320)
	if kerrors.FromError(err).GetCode() != 503 {
		t.Fatalf("err = %v, want 503", err)
	}
}

func TestReserveBuffer(t *testing.T) {
	ctx := context.Background()
	th := newThumbnailer(1, 100, 20*time.Millisecond)

	release, err := th.ReserveBuffer(ctx, 60)
	if err != nil {
		t.Fatal(err)
	}
	// 剩余 40，预留 50 要等待，超时返回 503
	if _, err := th.ReserveBuffer(ctx, 50); kerrors.FromError(err).GetCode() != 503 {
		t.Fatalf("over budget: err = %v, want 503", err)
	}
	r2, err := th.ReserveBuffer(ctx, 40)
	if err != nil {
		t.Fatalf("within budget: %v", err)
	}
	release()
	r2()

	// 超过预算的请求按预算计：独占，而不是永远等不到
	r3, err := th.ReserveBuffer(ctx, 1000)
	if err != nil {
		t.Fatalf("larger than budget: %v", err)
	}
	if _, err := th.ReserveBuffer(ctx, 1); kerrors.FromError(err).GetCode() != 503 {
		t.Fatalf("budget held exclusively: err = %v, want 503", err)
	}
	r3()

	// 调用方的 ctx 取消时返回 ctx 的错误，不当作依赖故障
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	hold, _ := th.ReserveBuffer(ctx, 100)
	defer hold()
	if _, err := th.ReserveBuffer(cctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: err = %v, want context.Canceled", err)
	}
}

func TestFitSize(t *testing.T) {
	cases := []struct{ w, h, max, wantW, wantH int }{
		{100, 50, 320, 100, 50},
		{640, 320, 320, 320, 160},
		{320, 640, 320, 160, 320},
		{1, 16_000_000, 320, 1, 320},
		{16_000_000, 1, 320, 320, 1},
	}
	for _, c := range cases {
		if w, h := fitSize(c.w, c.h, c.max); w != c.wantW || h != c.wantH {
			t.Errorf("fitSize(%d,%d,%d) = %d,%d, want %d,%d", c.w, c.h, c.max, w, h, c.wantW, c.wantH)
		}
	}
}
