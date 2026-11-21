package biz

import (
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxNameBytes = 255
	defaultName  = "file"
)

// rasterImageTypes 是按图片处理（解码、生成缩略图、内联显示）的类型，取自 http.DetectContentType 的结果。
// SVG 不在其中：它可以携带脚本，按普通文件下载。
var rasterImageTypes = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/gif":  true,
	"image/webp": true,
}

func isRasterImage(mimeType string) bool {
	return rasterImageTypes[mimeType]
}

// SanitizeName 清理客户端上送的文件名：去掉路径部分、无效 UTF-8 与控制字符和首尾空白，
// 按 UTF-8 字符边界截断到 255 字节，为空时用 file。文件名只用于展示和下载时的默认文件名，不参与存储路径。
func SanitizeName(raw string) string {
	s := strings.ToValidUTF8(raw, "")
	// Windows 客户端可能上送 C:\dir\a.txt
	s = strings.ReplaceAll(s, `\`, "/")
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == utf8.RuneError {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if len(s) > maxNameBytes {
		cut := maxNameBytes
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = strings.TrimSpace(s[:cut])
	}
	if s == "" || s == "." || s == ".." {
		return defaultName
	}
	return s
}

// ServeHeaders 决定下载响应的 Content-Type 与 Content-Disposition：
//   - 缩略图与成功解码的光栅图片内联显示，类型为识别出的图片类型；
//   - 其他文件（包括 SVG、HTML 和解码失败的图片）一律作为附件、以 application/octet-stream 下载，
//     浏览器不会把它们当作页面执行，避免存储型 XSS。
func ServeHeaders(f *File, variant int32) (contentType, disposition string) {
	switch {
	case variant == VariantThumbnail:
		stem := strings.TrimSuffix(f.Name, path.Ext(f.Name))
		if stem == "" {
			stem = defaultName
		}
		return "image/jpeg", ContentDisposition("inline", stem+".jpg")
	case f.Kind == KindImage && isRasterImage(f.MimeType):
		return f.MimeType, ContentDisposition("inline", f.Name)
	default:
		return "application/octet-stream", ContentDisposition("attachment", f.Name)
	}
}

// ContentDisposition 按 RFC 6266 生成 Content-Disposition：filename 为 ASCII 兜底，
// filename* 为 RFC 5987 编码的 UTF-8 原名，现代浏览器优先使用后者。
func ContentDisposition(dispositionType, name string) string {
	return dispositionType + `; filename="` + asciiFallback(name) + `"; filename*=UTF-8''` + encodeRFC5987(name)
}

// asciiFallback 把非 ASCII、控制字符以及引号、反斜杠、百分号替换为下划线，可以放进带引号的 filename 参数。
func asciiFallback(name string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' || r == '%' {
			return '_'
		}
		return r
	}, name)
}

// encodeRFC5987 对 attr-char 以外的字节做百分号编码。
func encodeRFC5987(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isAttrChar(c) {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0x0f])
	}
	return b.String()
}

// isAttrChar 是 RFC 5987 的 attr-char：字母、数字和 !#$&+-.^_`|~。
func isAttrChar(c byte) bool {
	switch {
	case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		return true
	}
	return strings.IndexByte("!#$&+-.^_`|~", c) >= 0
}
