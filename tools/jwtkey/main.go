// jwtkey 生成开发用的 Ed25519 密钥对，追加到 .env：
//
//	MARSCHAT_JWT_PRIVATE_KEY  私钥种子（32 字节）的 base64，只有 account 使用
//	MARSCHAT_JWT_PUBLIC_KEY   公钥的 base64，所有服务用来校验访问令牌
//
// .env 中已有这两个变量时不做任何修改，避免覆盖后已签发的令牌全部失效。
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

var existing = regexp.MustCompile(`(?m)^MARSCHAT_JWT_(PRIVATE|PUBLIC)_KEY=`)

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
	if existing.Match(content) {
		fmt.Printf("%s already contains JWT keys, nothing changed\n", path)
		return nil
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if len(content) > 0 && !bytes.HasSuffix(content, []byte("\n")) {
		buf.WriteByte('\n')
	}
	fmt.Fprintf(&buf, "MARSCHAT_JWT_PRIVATE_KEY=%s\n", base64.StdEncoding.EncodeToString(priv.Seed()))
	fmt.Fprintf(&buf, "MARSCHAT_JWT_PUBLIC_KEY=%s\n", base64.StdEncoding.EncodeToString(pub))

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Printf("JWT key pair appended to %s\n", path)
	return nil
}
