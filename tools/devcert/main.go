// devcert 为本地网关生成自签证书（ECDSA P-256，有效期 1 年），覆盖 localhost、127.0.0.1 和 ::1，
// 以及 -hosts 指定的局域网地址或域名（make certs 从 .env 的 MARSCHAT_CERT_HOSTS 读取）：
//
//	<目录>/tls.crt  证书（PEM）
//	<目录>/tls.key  私钥（PEM，权限 0600）
//
// 两个文件都已存在时不做任何修改：可能是换上去的正式证书，不能覆盖。已有证书没有覆盖 -hosts 中的某个名字时
// 只打印提示，需要重新生成时先删除这两个文件。目录被 git 忽略，私钥不进仓库。
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
	"strings"
	"time"
)

func main() {
	dir := flag.String("out", "deploy/apisix/certs", "证书输出目录")
	hosts := flag.String("hosts", "", "额外的 IP 或域名，逗号分隔，例如 192.168.1.10,marschat.lan")
	flag.Parse()
	if err := run(*dir, *hosts); err != nil {
		fmt.Fprintln(os.Stderr, "devcert:", err)
		os.Exit(1)
	}
}

func run(dir, hostList string) error {
	names, ips, err := parseHosts(hostList)
	if err != nil {
		return err
	}
	certPath := filepath.Join(dir, "tls.crt")
	keyPath := filepath.Join(dir, "tls.key")
	if exists(certPath) && exists(keyPath) {
		fmt.Printf("%s already contains tls.crt and tls.key, nothing changed\n", dir)
		missing, err := uncovered(certPath, names, ips)
		if err != nil {
			return err
		}
		if len(missing) > 0 {
			fmt.Printf("warning: the existing certificate does not cover %s; delete %s and %s, then run make certs again to regenerate\n",
				strings.Join(missing, ", "), certPath, keyPath)
		}
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
		DNSNames:     append([]string{"localhost"}, names...),
		IPAddresses:  append([]net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}, ips...),
		// 提前一小时生效，容忍容器与宿主机之间的时钟偏差
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(1, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		// 自签证书同时充当 CA，客户端用 --cacert 指定它、或在设备上信任它即可校验通过
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
	fmt.Printf("wrote %s and %s for %s (valid until %s)\n", certPath, keyPath,
		strings.Join(append(tmpl.DNSNames, ipStrings(tmpl.IPAddresses)...), ", "), tmpl.NotAfter.Format(time.DateOnly))
	return nil
}

// parseHosts 把逗号分隔的列表分成域名和 IP，忽略空项与默认已包含的名字。
func parseHosts(list string) (names []string, ips []net.IP, err error) {
	for h := range strings.SplitSeq(list, ",") {
		h = strings.TrimSpace(h)
		switch {
		case h == "" || h == "localhost":
		case net.ParseIP(h) != nil:
			if ip := net.ParseIP(h); !ip.IsLoopback() {
				ips = append(ips, ip)
			}
		case strings.ContainsAny(h, "/: ") || strings.HasPrefix(h, "."):
			return nil, nil, fmt.Errorf("invalid host %q: use a bare IP or domain name, without scheme or port", h)
		default:
			names = append(names, strings.ToLower(h))
		}
	}
	return names, ips, nil
}

// uncovered 返回已有证书没有覆盖的名字。
func uncovered(certPath string, names []string, ips []net.IP) ([]string, error) {
	data, err := os.ReadFile(certPath)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("%s is not a PEM certificate", certPath)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", certPath, err)
	}
	var missing []string
	for _, n := range names {
		if cert.VerifyHostname(n) != nil {
			missing = append(missing, n)
		}
	}
	for _, ip := range ips {
		if cert.VerifyHostname(ip.String()) != nil {
			missing = append(missing, ip.String())
		}
	}
	return missing, nil
}

func ipStrings(ips []net.IP) []string {
	out := make([]string, len(ips))
	for i, ip := range ips {
		out[i] = ip.String()
	}
	return out
}

func writePEM(path, typ string, der []byte, perm os.FileMode) error {
	return os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), perm)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return !errors.Is(err, os.ErrNotExist)
}
