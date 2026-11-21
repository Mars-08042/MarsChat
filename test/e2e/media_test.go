//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg" // 注册 JPEG 解码器，校验缩略图格式
	"image/png"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	kerrors "github.com/go-kratos/kratos/v3/errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	mediav1 "github.com/Mars-08042/MarsChat/api/media/v1"
	messagev1 "github.com/Mars-08042/MarsChat/api/message/v1"
	"github.com/Mars-08042/MarsChat/test/testkit"
)

// media 使用默认的本地存储（仓库下的 data/media），下载地址前缀为 http://127.0.0.1:18050。
// 发送图片、文件消息时 message 经 Consul 调用 media，所以 media 也要启动并完成迁移。
const (
	mediaHTTP = "http://127.0.0.1:18050"
	mediaGRPC = "127.0.0.1:19050"
)

func testPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			img.Set(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func item(f *mediav1.FileInfo, key string, v mediav1.Variant) *mediav1.DownloadUrlRequest {
	return &mediav1.DownloadUrlRequest{FileId: f.GetId(), AccessKey: key, Variant: v}
}

func fetch(t *testing.T, rawURL string, hdr map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res, body
}

func TestMediaUploadAndDownload(t *testing.T) {
	resetRateLimits(t)
	owner, other := newUser(t), newUser(t)

	content := make([]byte, 300_000)
	_, _ = rand.Read(content)
	doc := testkit.MustUpload(t, nil, mediaHTTP, owner.token, mediav1.Purpose_PURPOSE_CHAT, "季度报告.bin", content)
	if doc.GetName() != "季度报告.bin" || doc.GetSize() != int64(len(content)) || doc.GetKind() != mediav1.FileKind_FILE_KIND_FILE ||
		doc.GetMimeType() != "application/octet-stream" || doc.GetUploaderId() != owner.id || len(doc.GetAccessKey()) != 22 {
		t.Fatalf("unexpected file info %v", doc)
	}

	urls := testkit.DownloadURLs(t, mediaHTTP, other.token, item(doc, doc.GetAccessKey(), mediav1.Variant_VARIANT_ORIGINAL))
	if len(urls) != 1 || !strings.HasPrefix(urls[0].GetUrl(), mediaHTTP+"/v1/files/") {
		t.Fatalf("urls = %v", urls)
	}
	if d := time.Until(urls[0].GetExpiresAt().AsTime()); d < 9*time.Minute || d > 10*time.Minute {
		t.Fatalf("expires in %v", d)
	}
	res, body := fetch(t, urls[0].GetUrl(), nil)
	if res.StatusCode != http.StatusOK || !bytes.Equal(body, content) {
		t.Fatalf("download status %d, %d bytes", res.StatusCode, len(body))
	}
	if res.Header.Get("Content-Type") != "application/octet-stream" || res.Header.Get("X-Content-Type-Options") != "nosniff" ||
		!strings.Contains(res.Header.Get("Content-Disposition"), "attachment;") {
		t.Fatalf("headers %v", res.Header)
	}
	res, body = fetch(t, urls[0].GetUrl(), map[string]string{"Range": "bytes=100-199"})
	if res.StatusCode != http.StatusPartialContent || !bytes.Equal(body, content[100:200]) {
		t.Fatalf("range status %d, %d bytes", res.StatusCode, len(body))
	}

	// 篡改签名
	u, _ := url.Parse(urls[0].GetUrl())
	q := u.Query()
	q.Set("expires", fmt.Sprint(time.Now().Add(time.Hour).Unix()))
	u.RawQuery = q.Encode()
	res, body = fetch(t, u.String(), nil)
	if res.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "DOWNLOAD_URL_INVALID") {
		t.Fatalf("tampered url: %d %s", res.StatusCode, body)
	}

	// 无密钥、错误密钥的项被省略；上传者不带密钥也可以
	if got := testkit.DownloadURLs(t, mediaHTTP, other.token,
		item(doc, "", mediav1.Variant_VARIANT_ORIGINAL),
		item(doc, "AAAAAAAAAAAAAAAAAAAAAA", mediav1.Variant_VARIANT_ORIGINAL),
		item(doc, doc.GetAccessKey(), mediav1.Variant_VARIANT_THUMBNAIL), // 普通文件没有缩略图
		&mediav1.DownloadUrlRequest{FileId: 1, AccessKey: doc.GetAccessKey(), Variant: mediav1.Variant_VARIANT_ORIGINAL},
	); len(got) != 0 {
		t.Fatalf("unauthorized items returned: %v", got)
	}
	if got := testkit.DownloadURLs(t, mediaHTTP, owner.token, item(doc, "", mediav1.Variant_VARIANT_ORIGINAL)); len(got) != 1 {
		t.Fatalf("uploader without key: %v", got)
	}

	// 未登录
	if status, e := callAt(t, mediaHTTP, http.MethodPost, "/v1/files/download-urls", "",
		&mediav1.GetDownloadUrlsRequest{Items: []*mediav1.DownloadUrlRequest{item(doc, "", mediav1.Variant_VARIANT_ORIGINAL)}}, nil); e == nil || status != 401 {
		t.Fatalf("anonymous download-urls: %d %+v", status, e)
	}
	if _, status, e := testkit.Upload(t, nil, mediaHTTP, "", mediav1.Purpose_PURPOSE_CHAT, "a.txt", []byte("x")); e == nil || status != 401 {
		t.Fatalf("anonymous upload: %d %+v", status, e)
	}
}

func TestMediaImageAndAvatar(t *testing.T) {
	resetRateLimits(t)
	owner, other := newUser(t), newUser(t)

	img := testkit.MustUpload(t, nil, mediaHTTP, owner.token, mediav1.Purpose_PURPOSE_CHAT, "cat.png", testPNG(t, 640, 480))
	if img.GetKind() != mediav1.FileKind_FILE_KIND_IMAGE || !img.GetHasThumbnail() || img.GetWidth() != 640 || img.GetHeight() != 480 {
		t.Fatalf("unexpected image info %v", img)
	}
	urls := testkit.DownloadURLs(t, mediaHTTP, other.token,
		item(img, img.GetAccessKey(), mediav1.Variant_VARIANT_THUMBNAIL),
		item(img, img.GetAccessKey(), mediav1.Variant_VARIANT_ORIGINAL))
	if len(urls) != 2 {
		t.Fatalf("urls = %v", urls)
	}
	res, body := fetch(t, urls[0].GetUrl(), nil)
	cfg, format, err := image.DecodeConfig(bytes.NewReader(body))
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "image/jpeg" || err != nil || format != "jpeg" ||
		cfg.Width != 320 || cfg.Height != 240 || !strings.HasPrefix(res.Header.Get("Content-Disposition"), "inline;") {
		t.Fatalf("thumbnail: %d %v %s %dx%d %v", res.StatusCode, res.Header, format, cfg.Width, cfg.Height, err)
	}
	res, _ = fetch(t, urls[1].GetUrl(), nil)
	if res.Header.Get("Content-Type") != "image/png" || !strings.HasPrefix(res.Header.Get("Content-Disposition"), "inline;") {
		t.Fatalf("original image headers %v", res.Header)
	}

	// 头像所有登录用户都可以下载，不需要密钥
	ava := testkit.MustUpload(t, nil, mediaHTTP, owner.token, mediav1.Purpose_PURPOSE_AVATAR, "me.png", testPNG(t, 100, 100))
	if got := testkit.DownloadURLs(t, mediaHTTP, other.token, item(ava, "", mediav1.Variant_VARIANT_THUMBNAIL)); len(got) != 1 {
		t.Fatalf("avatar urls = %v", got)
	}

	// 头像必须是图片；超过 5 MiB 在读取请求体之前拒绝
	_, status, e := testkit.Upload(t, nil, mediaHTTP, owner.token, mediav1.Purpose_PURPOSE_AVATAR, "a.txt", []byte("not an image"))
	wantError(t, status, e, 415, "UNSUPPORTED_FILE_TYPE")
	_, status, e = testkit.Upload(t, nil, mediaHTTP, owner.token, mediav1.Purpose_PURPOSE_AVATAR, "big.png", make([]byte, 5<<20+1))
	wantError(t, status, e, 413, "FILE_TOO_LARGE")
	if e.Metadata["max_bytes"] != "5242880" {
		t.Fatalf("metadata %v", e.Metadata)
	}
}

func mediaInternal(t *testing.T) mediav1.MediaInternalServiceClient {
	t.Helper()
	conn, err := grpc.NewClient(mediaGRPC, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return mediav1.NewMediaInternalServiceClient(conn)
}

func sendContent(c messagev1.MessageInternalServiceClient, sender, peer int64, clientID string, content *messagev1.MessageContent) (*messagev1.SendResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := c.SendMessage(ctx, &messagev1.SendMessageRequest{
		SenderId: sender, SessionId: 1, ConnectionId: 1,
		Message: &messagev1.OutgoingMessage{
			PeerType: messagev1.ConversationType_CONVERSATION_TYPE_SINGLE, PeerId: peer, ClientMsgId: clientID, Content: content,
		},
	})
	return resp.GetResult(), err
}

func TestMessageWithImageAndFile(t *testing.T) {
	resetRateLimits(t)
	a, b := newUser(t), newUser(t)
	makeFriends(t, a, b)
	img := testkit.MustUpload(t, nil, mediaHTTP, a.token, mediav1.Purpose_PURPOSE_CHAT, "cat.png", testPNG(t, 64, 48))
	doc := testkit.MustUpload(t, nil, mediaHTTP, a.token, mediav1.Purpose_PURPOSE_CHAT, "notes.txt", []byte("meeting notes\n"))

	// 内部接口返回访问密钥与上传者，供 message 核实
	files, err := mediaInternal(t).GetFiles(context.Background(), &mediav1.GetFilesRequest{Ids: []int64{doc.GetId(), img.GetId(), 1}})
	if err != nil || len(files.GetFiles()) != 2 || files.GetFiles()[0].GetId() != doc.GetId() || files.GetFiles()[0].GetAccessKey() != doc.GetAccessKey() {
		t.Fatalf("GetFiles = %v, %v", files, err)
	}

	c := messageInternal(t)
	// 客户端伪造的名称、大小、类型、尺寸被 media 的记录覆盖
	r1, err := sendContent(c, a.id, b.id, "img", &messagev1.MessageContent{Body: &messagev1.MessageContent_Image{Image: &messagev1.ImageContent{
		FileId: img.GetId(), AccessKey: img.GetAccessKey(), Name: "evil.html", Size: 1, MimeType: "text/html", Width: 9999, Height: 1,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sendContent(c, b.id, a.id, "doc", &messagev1.MessageContent{Body: &messagev1.MessageContent_File{File: &messagev1.FileContent{
		FileId: doc.GetId(), AccessKey: doc.GetAccessKey(), Name: "evil.exe",
	}}}); err != nil {
		// b 不是上传者，凭访问密钥同样可以发送（转发场景）
		t.Fatal(err)
	}

	var hist messagev1.ListMessagesResponse
	mustMsg(t, http.MethodGet, fmt.Sprintf("/v1/conversations/%d/messages", r1.GetConversationId()), b.token, nil, &hist)
	if len(hist.GetMessages()) != 2 {
		t.Fatalf("history = %v", &hist)
	}
	gotFile, gotImg := hist.GetMessages()[0].GetContent().GetFile(), hist.GetMessages()[1].GetContent().GetImage()
	if gotImg.GetName() != "cat.png" || gotImg.GetSize() != img.GetSize() || gotImg.GetMimeType() != "image/png" ||
		gotImg.GetWidth() != 64 || gotImg.GetHeight() != 48 || gotImg.GetAccessKey() != img.GetAccessKey() {
		t.Fatalf("image content = %v", gotImg)
	}
	if gotFile.GetName() != "notes.txt" || gotFile.GetSize() != doc.GetSize() || gotFile.GetMimeType() != "text/plain; charset=utf-8" {
		t.Fatalf("file content = %v", gotFile)
	}
	// 接收方凭消息里的 ID 与密钥换取下载地址
	if urls := testkit.DownloadURLs(t, mediaHTTP, b.token,
		&mediav1.DownloadUrlRequest{FileId: gotImg.GetFileId(), AccessKey: gotImg.GetAccessKey(), Variant: mediav1.Variant_VARIANT_THUMBNAIL}); len(urls) != 1 {
		t.Fatalf("receiver urls = %v", urls)
	}

	// 无效引用：不存在、密钥错误、图片消息引用普通文件
	bad := []*messagev1.MessageContent{
		{Body: &messagev1.MessageContent_File{File: &messagev1.FileContent{FileId: 1, AccessKey: doc.GetAccessKey()}}},
		{Body: &messagev1.MessageContent_File{File: &messagev1.FileContent{FileId: doc.GetId(), AccessKey: img.GetAccessKey()}}},
		{Body: &messagev1.MessageContent_Image{Image: &messagev1.ImageContent{FileId: doc.GetId(), AccessKey: doc.GetAccessKey()}}},
	}
	for i, content := range bad {
		_, err := sendContent(c, a.id, b.id, fmt.Sprintf("bad-%d", i), content)
		if kerrors.FromError(err).GetReason() != messagev1.ErrorReason_FILE_INVALID.String() || kerrors.FromError(err).GetCode() != 400 {
			t.Fatalf("bad content %d: %v", i, err)
		}
	}
}
