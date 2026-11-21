// devcert 为本地网关生成自签证书（ECDSA P-256，有效期 1 年），覆盖 localhost、127.0.0.1 和 ::1：
//
//	<目录>/tls.crt  证书（PEM）
//	<目录>/tls.key  私钥（PEM，权限 0600）
//
// 两个文件都已存在时不做任何修改。目录被 git 忽略，私钥不进仓库。
// 证书只用于本地演示 HTTPS 和 WSS，浏览器和 curl 需要显式信任它（curl --cacert）。
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

func main() {
	dir := flag.String("out", "deploy/apisix/certs", "证书输出目录")
	flag.Parse()
	if err := run(*dir); err != nil {
		fmt.Fprintln(os.Stderr, "devcert:", err)
		os.Exit(1)
	}
}

func run(dir string) error {
	certPath := filepath.Join(dir, "tls.crt")
	keyPath := filepath.Join(dir, "tls.key")
	if exists(certPath) && exists(keyPath) {
		fmt.Printf("%s already contains tls.crt and tls.key, nothing changed\n", dir)
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "localhost", Organization: []string{"MarsChat dev"}},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
		// 提前一小时生效，容忍容器与宿主机之间的时钟偏差
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(1, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		// 自签证书同时充当 CA，客户端用 --cacert 指定它即可校验通过
		IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}

	if err := writePEM(keyPath, "PRIVATE KEY", keyDER, 0o600); err != nil {
		return err
	}
	if err := writePEM(certPath, "CERTIFICATE", der, 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s and %s (valid until %s)\n", certPath, keyPath, tmpl.NotAfter.Format(time.DateOnly))
	return nil
}

func writePEM(path, typ string, der []byte, perm os.FileMode) error {
	return os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), perm)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return !errors.Is(err, os.ErrNotExist)
}
