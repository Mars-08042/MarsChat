package main

import (
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseHosts(t *testing.T) {
	names, ips, err := parseHosts(" 192.168.1.10, Marschat.LAN ,,localhost,127.0.0.1,::1,fd00::5")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(names, ",") != "marschat.lan" {
		t.Errorf("names = %v, want [marschat.lan]", names)
	}
	if got := strings.Join(ipStrings(ips), ","); got != "192.168.1.10,fd00::5" {
		t.Errorf("ips = %v, want 192.168.1.10 and fd00::5 (loopback is always included)", got)
	}
	for _, bad := range []string{"https://a.example", "a.example:443", ".example"} {
		if _, _, err := parseHosts(bad); err == nil {
			t.Errorf("parseHosts(%q) accepted an invalid host", bad)
		}
	}
}

func readCert(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(data)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

// 生成的证书覆盖默认名字和额外的局域网地址、域名；再次运行不覆盖已有证书，换了名字时只提示。
func TestRunGeneratesAndKeeps(t *testing.T) {
	dir := t.TempDir()
	if err := run(dir, "192.168.1.10,marschat.lan"); err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(dir, "tls.crt")
	cert := readCert(t, certPath)
	for _, h := range []string{"localhost", "127.0.0.1", "::1", "192.168.1.10", "marschat.lan"} {
		if err := cert.VerifyHostname(h); err != nil {
			t.Errorf("certificate does not cover %s: %v", h, err)
		}
	}

	if err := run(dir, "192.168.1.20"); err != nil {
		t.Fatal(err)
	}
	if again := readCert(t, certPath); again.SerialNumber.Cmp(cert.SerialNumber) != 0 {
		t.Fatal("existing certificate was overwritten")
	}
	missing, err := uncovered(certPath, nil, mustIPs(t, "192.168.1.20"))
	if err != nil || strings.Join(missing, ",") != "192.168.1.20" {
		t.Fatalf("uncovered = %v, %v; want [192.168.1.20]", missing, err)
	}
}

func mustIPs(t *testing.T, list string) []net.IP {
	t.Helper()
	_, ips, err := parseHosts(list)
	if err != nil {
		t.Fatal(err)
	}
	return ips
}
