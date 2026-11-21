package biz

import (
	"context"
	"fmt"
	"testing"

	"google.golang.org/protobuf/proto"

	v1 "github.com/Mars-08042/MarsChat/api/message/v1"
)

const (
	imageID int64 = 501
	docID   int64 = 502
	avatar  int64 = 503
)

func addFiles(e *env) {
	e.files.files[imageID] = &FileMeta{ID: imageID, AccessKey: "img-key", Purpose: FilePurposeChat, Kind: FileKindImage,
		Name: "cat.png", Size: 2048, MimeType: "image/png", Width: 800, Height: 600, HasThumbnail: true}
	e.files.files[docID] = &FileMeta{ID: docID, AccessKey: "doc-key", Purpose: FilePurposeChat, Kind: 1,
		Name: "report.pdf", Size: 4096, MimeType: "application/pdf"}
	e.files.files[avatar] = &FileMeta{ID: avatar, AccessKey: "ava-key", Purpose: 2, Kind: FileKindImage,
		Name: "me.png", Size: 100, MimeType: "image/png", Width: 10, Height: 10, HasThumbnail: true}
}

func imageContent(id int64, key string) *v1.MessageContent {
	// 客户端伪造的展示字段，应被 media 的记录覆盖
	return &v1.MessageContent{Body: &v1.MessageContent_Image{Image: &v1.ImageContent{
		FileId: id, AccessKey: key, Name: "fake.exe", Size: 1, MimeType: "text/html", Width: 1, Height: 1,
	}}}
}

func fileContent(id int64, key string) *v1.MessageContent {
	return &v1.MessageContent{Body: &v1.MessageContent_File{File: &v1.FileContent{
		FileId: id, AccessKey: key, Name: "fake.exe", Size: 1, MimeType: "text/html",
	}}}
}

func (e *env) sendContent(content *v1.MessageContent, clientID string) (*Message, error) {
	return e.msg.Send(context.Background(), SendInput{
		SenderID: alice, ConnectionID: 1, PeerType: TypeSingle, PeerID: bob, ClientMsgID: clientID, Content: content,
	})
}

func TestSendImageAndFileOverwritesMetadata(t *testing.T) {
	e := newEnv(t)
	addFiles(e)

	m, err := e.sendContent(imageContent(imageID, "img-key"), "img")
	if err != nil {
		t.Fatal(err)
	}
	want := &v1.ImageContent{FileId: imageID, AccessKey: "img-key", Name: "cat.png", Size: 2048, MimeType: "image/png", Width: 800, Height: 600}
	if !proto.Equal(m.Content.GetImage(), want) {
		t.Fatalf("image content = %v, want %v", m.Content.GetImage(), want)
	}
	// 事件中的内容与落库的一致
	if ev := e.lastEvent(t).(*v1.MessageCreated); !proto.Equal(ev.GetMessage().GetContent().GetImage(), want) {
		t.Fatalf("event content = %v", ev.GetMessage().GetContent())
	}

	m, err = e.sendContent(fileContent(docID, "doc-key"), "doc")
	if err != nil {
		t.Fatal(err)
	}
	wantFile := &v1.FileContent{FileId: docID, AccessKey: "doc-key", Name: "report.pdf", Size: 4096, MimeType: "application/pdf"}
	if !proto.Equal(m.Content.GetFile(), wantFile) {
		t.Fatalf("file content = %v, want %v", m.Content.GetFile(), wantFile)
	}

	// 图片也可以作为文件发送
	if _, err := e.sendContent(fileContent(imageID, "img-key"), "img-as-file"); err != nil {
		t.Fatal(err)
	}
}

func TestSendFileInvalid(t *testing.T) {
	cases := []struct {
		name    string
		content *v1.MessageContent
	}{
		{"unknown file", fileContent(999, "doc-key")},
		{"wrong key", fileContent(docID, "img-key")},
		{"empty key", fileContent(docID, "")},
		{"avatar is not a chat file", fileContent(avatar, "ava-key")},
		{"image message with plain file", imageContent(docID, "doc-key")},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			addFiles(e)
			_, err := e.sendContent(tc.content, fmt.Sprintf("bad-%d", i))
			wantReason(t, err, v1.ErrorReason_FILE_INVALID.String())
			if len(e.s.msgs) != 0 {
				t.Fatal("rejected message must not be written")
			}
		})
	}
}

func TestSendFileMediaUnavailable(t *testing.T) {
	e := newEnv(t)
	addFiles(e)
	e.files.err = fmt.Errorf("%w: media down", ErrUnavailable)
	_, err := e.sendContent(fileContent(docID, "doc-key"), "doc")
	wantReason(t, err, v1.ErrorReason_DEPENDENCY_UNAVAILABLE.String())

	// 文本消息不调用 media，不受影响
	calls := e.files.calls
	e.send(t, alice, TypeSingle, bob, "text")
	if e.files.calls != calls {
		t.Fatal("text message must not call media")
	}
}
