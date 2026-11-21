// jwtkey 生成开发用的密钥，追加到 .env：
//
//	MARSCHAT_JWT_PRIVATE_KEY  Ed25519 私钥种子（32 字节）的 base64，只有 account 使用
//	MARSCHAT_JWT_PUBLIC_KEY   公钥的 base64，所有服务用来校验访问令牌
//	MARSCHAT_MEDIA_URL_SECRET 32 字节随机数的 base64，media 本地存储用来签名下载地址
//
// 每组密钥单独判断：.env 中已有的不做任何修改，避免覆盖后已签发的令牌或下载地址全部失效。
// 不打印密钥内容。
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"regexp"
)

var (
	existingJWT       = regexp.MustCompile(`(?m)^MARSCHAT_JWT_(PRIVATE|PUBLIC)_KEY=`)
	existingURLSecret = regexp.MustCompile(`(?m)^MARSCHAT_MEDIA_URL_SECRET=.+`)
)

func main() {
	path := flag.String("env", ".env", "要写入的 .env 文件")
	flag.Parse()
	if err := run(*path); err != nil {
		fmt.Fprintln(os.Stderr, "jwtkey:", err)
		os.Exit(1)
	}
}

func run(path string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s (copy .env.example to .env first): %w", path, err)
	}

	var buf bytes.Buffer
	var added []string
	if existingJWT.Match(content) {
		fmt.Printf("%s already contains JWT keys, kept\n", path)
	} else {
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		fmt.Fprintf(&buf, "MARSCHAT_JWT_PRIVATE_KEY=%s\n", base64.StdEncoding.EncodeToString(priv.Seed()))
		fmt.Fprintf(&buf, "MARSCHAT_JWT_PUBLIC_KEY=%s\n", base64.StdEncoding.EncodeToString(pub))
		added = append(added, "JWT key pair")
	}
	if existingURLSecret.Match(content) {
		fmt.Printf("%s already contains MARSCHAT_MEDIA_URL_SECRET, kept\n", path)
	} else {
		secret := make([]byte, 32)
		_, _ = rand.Read(secret) // crypto/rand.Read 不会返回错误
		fmt.Fprintf(&buf, "MARSCHAT_MEDIA_URL_SECRET=%s\n", base64.StdEncoding.EncodeToString(secret))
		added = append(added, "media URL secret")
	}
	if buf.Len() == 0 {
		return nil
	}
	out := buf.Bytes()
	if len(content) > 0 && !bytes.HasSuffix(content, []byte("\n")) {
		out = append([]byte("\n"), out...)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	if _, err := f.Write(out); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Printf("appended %v to %s\n", added, path)
	return nil
}
