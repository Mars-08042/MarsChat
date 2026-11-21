package testkit

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	mediav1 "github.com/Mars-08042/MarsChat/api/media/v1"
)

// Upload 调用 media 的上传接口（POST /v1/files，请求体为文件原始字节）。client 为 nil 时用 http.DefaultClient。
// 带 Expect: 100-continue：服务端在读请求体之前就拒绝（超限、类型不对）时，客户端不用先把整个文件发完。
// 成功时返回文件信息，失败时返回状态码与错误体。
func Upload(t testing.TB, client *http.Client, base, token string, purpose mediav1.Purpose, name string, data []byte) (*mediav1.FileInfo, int, *APIError) {
	t.Helper()
	if client == nil {
		client = http.DefaultClient
	}
	q := url.Values{"purpose": {purpose.String()}, "name": {name}}
	req, err := http.NewRequest(http.MethodPost, base+"/v1/files?"+q.Encode(), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Expect", "100-continue")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("upload %s: %v", name, err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		e := APIError{Code: res.StatusCode}
		_ = json.Unmarshal(body, &e)
		return nil, res.StatusCode, &e
	}
	var info mediav1.FileInfo
	if err := protojson.Unmarshal(body, &info); err != nil {
		t.Fatalf("upload %s: decode %s: %v", name, body, err)
	}
	return &info, res.StatusCode, nil
}

// MustUpload 上传并在失败时终止测试。
func MustUpload(t testing.TB, client *http.Client, base, token string, purpose mediav1.Purpose, name string, data []byte) *mediav1.FileInfo {
	t.Helper()
	info, status, e := Upload(t, client, base, token, purpose, name, data)
	if e != nil {
		t.Fatalf("upload %s: status %d, %+v", name, status, e)
	}
	return info
}

// DownloadURLs 换取下载地址，返回 url 列表（校验不通过的项被省略）。
func DownloadURLs(t testing.TB, base, token string, items ...*mediav1.DownloadUrlRequest) []*mediav1.DownloadUrl {
	t.Helper()
	var resp mediav1.GetDownloadUrlsResponse
	if status, e := Call(t, base, http.MethodPost, "/v1/files/download-urls", token,
		&mediav1.GetDownloadUrlsRequest{Items: items}, &resp); e != nil {
		t.Fatalf("download-urls: status %d, %+v", status, e)
	}
	return resp.GetUrls()
}
