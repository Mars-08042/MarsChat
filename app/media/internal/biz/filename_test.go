package biz

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizeName(t *testing.T) {
	long := strings.Repeat("文", 100) // 300 字节
	cases := []struct {
		name, in, want string
	}{
		{"plain", "report.pdf", "report.pdf"},
		{"unix path", "/etc/passwd", "passwd"},
		{"windows path", `C:\Users\a\图片.png`, "图片.png"},
		{"traversal", "../../secret.txt", "secret.txt"},
		{"control chars", "a\x00b\r\nc\t.txt", "abc.txt"},
		{"trim spaces", "  note.md  ", "note.md"},
		{"invalid utf8", "a\xffb.txt", "ab.txt"},
		{"empty", "", "file"},
		{"only path", "dir/", "file"},
		{"dot", ".", "file"},
		{"dotdot", "..", "file"},
		{"truncate at rune boundary", long, strings.Repeat("文", 85)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SanitizeName(tc.in)
			if got != tc.want {
				t.Fatalf("SanitizeName(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if len(got) > maxNameBytes || !utf8.ValidString(got) {
				t.Fatalf("result %q is too long or invalid UTF-8", got)
			}
		})
	}
}

func TestContentDisposition(t *testing.T) {
	got := ContentDisposition("attachment", `报告 "终版" 100%.pdf`)
	want := `attachment; filename="__ ____ 100_.pdf"; filename*=UTF-8''%E6%8A%A5%E5%91%8A%20%22%E7%BB%88%E7%89%88%22%20100%25.pdf`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	// attr-char 原样保留
	if got := encodeRFC5987("a-b_c.d~e!f"); got != "a-b_c.d~e!f" {
		t.Fatalf("encodeRFC5987 changed attr-chars: %s", got)
	}
}

func TestServeHeaders(t *testing.T) {
	image := &File{Name: "cat.png", Kind: KindImage, MimeType: "image/png", ThumbnailSize: 10}
	cases := []struct {
		name        string
		f           *File
		variant     int32
		contentType string
		dispPrefix  string
	}{
		{"decoded image inline", image, VariantOriginal, "image/png", `inline; filename="cat.png"`},
		{"thumbnail inline as jpg", image, VariantThumbnail, "image/jpeg", `inline; filename="cat.jpg"`},
		{"plain file attachment", &File{Name: "a.txt", Kind: KindFile, MimeType: "text/plain; charset=utf-8"}, VariantOriginal,
			"application/octet-stream", `attachment; filename="a.txt"`},
		{"html never inline", &File{Name: "x.html", Kind: KindFile, MimeType: "text/html; charset=utf-8"}, VariantOriginal,
			"application/octet-stream", "attachment;"},
		{"svg never inline", &File{Name: "x.svg", Kind: KindFile, MimeType: "text/xml; charset=utf-8"}, VariantOriginal,
			"application/octet-stream", "attachment;"},
		{"undecodable png attachment", &File{Name: "bad.png", Kind: KindFile, MimeType: "image/png"}, VariantOriginal,
			"application/octet-stream", "attachment;"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ct, disp := ServeHeaders(tc.f, tc.variant)
			if ct != tc.contentType || !strings.HasPrefix(disp, tc.dispPrefix) {
				t.Fatalf("got (%q, %q), want (%q, prefix %q)", ct, disp, tc.contentType, tc.dispPrefix)
			}
		})
	}
}
