//go:build cluster

package cluster

import (
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"image"
	"image/color"
	_ "image/jpeg" // 注册 JPEG 解码器，校验缩略图格式
	"image/png"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	accessv1 "github.com/Mars-08042/MarsChat/api/access/v1"
	mediav1 "github.com/Mars-08042/MarsChat/api/media/v1"
	messagev1 "github.com/Mars-08042/MarsChat/api/message/v1"
	"github.com/Mars-08042/MarsChat/test/testkit"
)

// 容器模式下 media 的下载地址前缀是网关的 HTTPS 地址（compose.yaml 中 MARSCHAT_MEDIA_PUBLIC_BASE_URL），
// 用 make certs 生成的自签证书访问。
func gatewayTLSClient(t *testing.T) *http.Client {
	t.Helper()
	pem, err := os.ReadFile("../../deploy/apisix/certs/tls.crt")
	if err != nil {
		t.Fatalf("read gateway certificate (run make certs): %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		t.Fatal("invalid certificate")
	}
	return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}},
		Timeout: 60 * time.Second}
}

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			img.Set(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: 90, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// 经网关上传、换取下载地址、下载；记录 20 MB 文件的上传与下载耗时。
func TestMediaThroughGateway(t *testing.T) {
	resetRateLimits(t)
	u := newUser(t)
	client := gatewayTLSClient(t)

	data := make([]byte, 20<<20)
	_, _ = rand.Read(data)
	start := time.Now()
	f := testkit.MustUpload(t, nil, gatewayHTTP, u.token, mediav1.Purpose_PURPOSE_CHAT, "big.bin", data)
	t.Logf("upload 20 MiB through gateway: %v", time.Since(start))

	urls := testkit.DownloadURLs(t, gatewayHTTP, u.token,
		&mediav1.DownloadUrlRequest{FileId: f.GetId(), AccessKey: f.GetAccessKey(), Variant: mediav1.Variant_VARIANT_ORIGINAL})
	if len(urls) != 1 || !strings.HasPrefix(urls[0].GetUrl(), "https://127.0.0.1:19443/v1/files/") {
		t.Fatalf("urls = %v", urls)
	}
	start = time.Now()
	res, err := client.Get(urls[0].GetUrl())
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	t.Logf("download 20 MiB through gateway: %v", time.Since(start))
	if res.StatusCode != http.StatusOK || !bytes.Equal(body, data) || res.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("download status %d, %d bytes, headers %v", res.StatusCode, len(body), res.Header)
	}

	// 超过网关的请求体上限（101 MiB）时由网关直接拒绝，不转发
	_, status, _ := testkit.Upload(t, nil, gatewayHTTP, u.token, mediav1.Purpose_PURPOSE_CHAT, "huge.bin", make([]byte, 101<<20+1))
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized upload through gateway: status %d, want 413", status)
	}
}

// 图片消息经网关的 WebSocket 发送：message 在容器网络内经 Consul 调用 media 核实文件，
// 推送给对方的内容带有 media 记录的元数据，对方凭消息里的访问密钥取到缩略图。
func TestImageMessageAcrossServices(t *testing.T) {
	resetRateLimits(t)
	a, b := newUser(t), newUser(t)
	makeFriends(t, a, b)
	img := testkit.MustUpload(t, nil, gatewayHTTP, a.token, mediav1.Purpose_PURPOSE_CHAT, "cat.png", pngBytes(t, 400, 300))

	a1, b1 := connect(t, a), connect(t, b)
	r := a1.Call(&accessv1.ClientFrame{RequestId: 7, Body: &accessv1.ClientFrame_SendMessage{SendMessage: &messagev1.OutgoingMessage{
		PeerType: messagev1.ConversationType_CONVERSATION_TYPE_SINGLE, PeerId: b.id, ClientMsgId: "img-1",
		Content: &messagev1.MessageContent{Body: &messagev1.MessageContent_Image{Image: &messagev1.ImageContent{
			FileId: img.GetId(), AccessKey: img.GetAccessKey(), Name: "forged.html",
		}}},
	}}})
	ack := r.GetSendAck()
	if ack == nil {
		t.Fatalf("send image: %v", r)
	}
	pushed := b1.WaitFrame("image message push", testkit.IsMessage(ack.GetConversationId(), ack.GetSeq())).GetMessage().GetContent().GetImage()
	if pushed.GetName() != "cat.png" || pushed.GetWidth() != 400 || pushed.GetHeight() != 300 || pushed.GetMimeType() != "image/png" {
		t.Fatalf("pushed image content = %v", pushed)
	}

	urls := testkit.DownloadURLs(t, gatewayHTTP, b.token,
		&mediav1.DownloadUrlRequest{FileId: pushed.GetFileId(), AccessKey: pushed.GetAccessKey(), Variant: mediav1.Variant_VARIANT_THUMBNAIL})
	if len(urls) != 1 {
		t.Fatalf("urls = %v", urls)
	}
	res, err := gatewayTLSClient(t).Get(urls[0].GetUrl())
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	cfg, format, err := image.DecodeConfig(res.Body)
	if res.StatusCode != http.StatusOK || err != nil || format != "jpeg" || cfg.Width != 320 || cfg.Height != 240 {
		t.Fatalf("thumbnail: status %d, %s %dx%d, %v", res.StatusCode, format, cfg.Width, cfg.Height, err)
	}

	// 伪造的文件 ID：message 返回 FILE_INVALID
	r = a1.Call(&accessv1.ClientFrame{RequestId: 8, Body: &accessv1.ClientFrame_SendMessage{SendMessage: &messagev1.OutgoingMessage{
		PeerType: messagev1.ConversationType_CONVERSATION_TYPE_SINGLE, PeerId: b.id, ClientMsgId: "img-2",
		Content: &messagev1.MessageContent{Body: &messagev1.MessageContent_File{File: &messagev1.FileContent{FileId: 1, AccessKey: img.GetAccessKey()}}},
	}}})
	if r.GetError().GetReason() != messagev1.ErrorReason_FILE_INVALID.String() {
		t.Fatalf("forged file id: %v", r)
	}
}
