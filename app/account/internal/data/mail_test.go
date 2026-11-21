package data

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"io"
	"log/slog"
	"math/big"
	"mime"
	"net"
	"net/mail"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/Mars-08042/MarsChat/app/account/internal/biz"
	"github.com/Mars-08042/MarsChat/app/account/internal/conf"
)

const (
	testFrom = "sender@qq.com"
	testTo   = "user@qq.com"
	testPass = "auth-code"
)

// fakeSMTP 是只实现发信所需命令的 SMTP 服务器，用于测试连接方式、认证和投递内容。
type fakeSMTP struct {
	t        *testing.T
	ln       net.Listener
	tlsConf  *tls.Config
	implicit bool // 隐式 TLS：监听端直接是 TLS
	starttls bool // 明文监听时是否提供 STARTTLS
	authCode string
	silent   bool // 接受连接后不发问候，模拟卡住的服务器

	mu       sync.Mutex
	authUser string
	authPass string
	from     string
	rcpt     string
	data     string
	sawTLS   bool
}

func newFakeSMTP(t *testing.T, cert tls.Certificate, implicit, starttls bool) *fakeSMTP {
	t.Helper()
	s := &fakeSMTP{
		t: t, tlsConf: &tls.Config{Certificates: []tls.Certificate{cert}},
		implicit: implicit, starttls: starttls, authCode: "235 2.7.0 Authentication successful",
	}
	var err error
	if implicit {
		s.ln, err = tls.Listen("tcp", "127.0.0.1:0", s.tlsConf)
	} else {
		s.ln, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.ln.Close() })
	go s.serve()
	return s
}

func (s *fakeSMTP) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *fakeSMTP) handle(conn net.Conn) {
	defer conn.Close()
	if s.silent {
		_, _ = io.Copy(io.Discard, conn)
		return
	}
	_, isTLS := conn.(*tls.Conn)
	r := bufio.NewReader(conn)
	w := func(line string) { _, _ = io.WriteString(conn, line+"\r\n") }
	w("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.TrimRight(line, "\r\n")
		upper := strings.ToUpper(cmd)
		switch {
		case strings.HasPrefix(upper, "EHLO"):
			w("250-fake")
			if s.starttls && !isTLS {
				w("250-STARTTLS")
			}
			w("250 AUTH PLAIN")
		case upper == "STARTTLS":
			w("220 Ready to start TLS")
			tc := tls.Server(conn, s.tlsConf)
			if err := tc.Handshake(); err != nil {
				return
			}
			conn, isTLS = tc, true
			r = bufio.NewReader(conn)
		case strings.HasPrefix(upper, "AUTH PLAIN"):
			raw, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(cmd[len("AUTH PLAIN"):]))
			parts := strings.Split(string(raw), "\x00")
			s.mu.Lock()
			if len(parts) == 3 {
				s.authUser, s.authPass = parts[1], parts[2]
			}
			s.sawTLS = isTLS
			s.mu.Unlock()
			w(s.authCode)
		case strings.HasPrefix(upper, "MAIL FROM:"):
			s.mu.Lock()
			s.from = strings.Trim(cmd[len("MAIL FROM:"):], "<> ")
			s.mu.Unlock()
			w("250 OK")
		case strings.HasPrefix(upper, "RCPT TO:"):
			s.mu.Lock()
			s.rcpt = strings.Trim(cmd[len("RCPT TO:"):], "<> ")
			s.mu.Unlock()
			w("250 OK")
		case upper == "DATA":
			w("354 End data with <CR><LF>.<CR><LF>")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			s.mu.Lock()
			s.data = b.String()
			s.mu.Unlock()
			w("250 OK queued")
		case upper == "QUIT":
			w("221 Bye")
			return
		default:
			w("502 Command not implemented")
		}
	}
}

// testCert 生成 127.0.0.1 的自签证书，返回服务端证书和信任它的客户端 TLS 配置。
func testCert(t *testing.T) (tls.Certificate, *tls.Config) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf},
		&tls.Config{ServerName: "127.0.0.1", RootCAs: pool, MinVersion: tls.VersionTLS12}
}

func testSender(t *testing.T, s *fakeSMTP, client *tls.Config, implicit bool) *smtpSender {
	t.Helper()
	return &smtpSender{
		addr: s.ln.Addr().String(), host: "127.0.0.1", username: testFrom, password: testPass,
		timeout: 2 * time.Second, implicitTLS: implicit, tlsConfig: client,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)), now: time.Now,
	}
}

func TestSMTPSenderImplicitTLS(t *testing.T) {
	cert, client := testCert(t)
	srv := newFakeSMTP(t, cert, true, false)
	if err := testSender(t, srv, client, true).Send(context.Background(), testTo, "123456", biz.PurposeRegister, 5*time.Minute); err != nil {
		t.Fatalf("send: %v", err)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if !srv.sawTLS || srv.authUser != testFrom || srv.authPass != testPass {
		t.Fatalf("auth over tls = %v, user %q, pass %q", srv.sawTLS, srv.authUser, srv.authPass)
	}
	if srv.from != testFrom || srv.rcpt != testTo {
		t.Fatalf("envelope from %q to %q", srv.from, srv.rcpt)
	}
	checkCodeMail(t, srv.data, "注册", "123456", 5)
}

func TestSMTPSenderStartTLS(t *testing.T) {
	cert, client := testCert(t)
	srv := newFakeSMTP(t, cert, false, true)
	if err := testSender(t, srv, client, false).Send(context.Background(), testTo, "654321", biz.PurposeLogin, 90*time.Second); err != nil {
		t.Fatalf("send: %v", err)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if !srv.sawTLS {
		t.Fatal("credentials were sent before STARTTLS")
	}
	// 90 秒向上取整为 2 分钟
	checkCodeMail(t, srv.data, "登录", "654321", 2)
}

func TestSMTPSenderRefusesPlainText(t *testing.T) {
	cert, client := testCert(t)
	srv := newFakeSMTP(t, cert, false, false)
	err := testSender(t, srv, client, false).Send(context.Background(), testTo, "123456", biz.PurposeLogin, time.Minute)
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("got %v, want an error about missing STARTTLS", err)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.authPass != "" {
		t.Fatal("credentials sent over a plain-text connection")
	}
}

func TestSMTPSenderAuthFailure(t *testing.T) {
	cert, client := testCert(t)
	srv := newFakeSMTP(t, cert, true, false)
	srv.authCode = "535 5.7.8 Authentication failed"
	err := testSender(t, srv, client, true).Send(context.Background(), testTo, "123456", biz.PurposeLogin, time.Minute)
	if err == nil || !strings.Contains(err.Error(), "smtp auth") {
		t.Fatalf("got %v, want an auth error", err)
	}
}

func TestSMTPSenderUntrustedCertificate(t *testing.T) {
	cert, _ := testCert(t)
	_, otherClient := testCert(t)
	srv := newFakeSMTP(t, cert, true, false)
	if err := testSender(t, srv, otherClient, true).Send(context.Background(), testTo, "123456", biz.PurposeLogin, time.Minute); err == nil {
		t.Fatal("sent through a server whose certificate is not trusted")
	}
}

// 服务器卡住时，发送在配置的超时与请求截止时间中较早的一个到达时失败。
func TestSMTPSenderDeadline(t *testing.T) {
	cert, client := testCert(t)
	srv := newFakeSMTP(t, cert, false, true)
	srv.silent = true

	s := testSender(t, srv, client, false)
	s.timeout = 300 * time.Millisecond
	begin := time.Now()
	if err := s.Send(context.Background(), testTo, "123456", biz.PurposeLogin, time.Minute); err == nil {
		t.Fatal("send to a stuck server succeeded")
	}
	if took := time.Since(begin); took > 2*time.Second {
		t.Fatalf("send took %v, want it bounded by the 300ms timeout", took)
	}

	s.timeout = 5 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	begin = time.Now()
	if err := s.Send(ctx, testTo, "123456", biz.PurposeLogin, time.Minute); err == nil {
		t.Fatal("send to a stuck server succeeded")
	}
	if took := time.Since(begin); took > 2*time.Second {
		t.Fatalf("send took %v, want it bounded by the 200ms request deadline", took)
	}
}

func TestSMTPSenderRejectsReservedDomains(t *testing.T) {
	cert, client := testCert(t)
	srv := newFakeSMTP(t, cert, true, false)
	s := testSender(t, srv, client, true)
	for _, to := range []string{"e2e-1@example.test", "u1@legacy.invalid", "a@example.com", "a@localhost", "a@x.example"} {
		if err := s.Send(context.Background(), to, "123456", biz.PurposeLogin, time.Minute); err == nil {
			t.Errorf("sent to reserved address %s", to)
		}
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.rcpt != "" {
		t.Fatalf("reserved address reached the server: %s", srv.rcpt)
	}
	if reservedDomain("someone@qq.com") || reservedDomain("a@notexample.com") {
		t.Fatal("real domains must not be treated as reserved")
	}
}

func TestNewSMTPSenderConfig(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	good := &conf.Smtp{Addr: "smtp.qq.com:465", Username: testFrom, Password: testPass}
	s, err := newSMTPSender(good, logger)
	if err != nil {
		t.Fatal(err)
	}
	if !s.implicitTLS || s.timeout != defaultSMTPTimeout || s.tlsConfig.ServerName != "smtp.qq.com" {
		t.Fatalf("port 465 should use implicit TLS with the default timeout: %+v", s)
	}
	s, err = newSMTPSender(&conf.Smtp{Addr: "smtp.163.com:587", Username: testFrom, Password: testPass, Timeout: durationpb.New(time.Second)}, logger)
	if err != nil {
		t.Fatal(err)
	}
	if s.implicitTLS || s.timeout != time.Second {
		t.Fatalf("port 587 should use STARTTLS with the configured timeout: %+v", s)
	}
	for name, c := range map[string]*conf.Smtp{
		"missing addr":      {Username: testFrom, Password: testPass},
		"addr without port": {Addr: "smtp.qq.com", Username: testFrom, Password: testPass},
		"missing username":  {Addr: "smtp.qq.com:465", Password: testPass},
		"missing password":  {Addr: "smtp.qq.com:465", Username: testFrom},
		"username not bare": {Addr: "smtp.qq.com:465", Username: "MarsChat <" + testFrom + ">", Password: testPass},
	} {
		if _, err := newSMTPSender(c, logger); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// checkCodeMail 解析收到的邮件，检查头部与正文。
func checkCodeMail(t *testing.T, data, action, code string, minutes int) {
	t.Helper()
	if strings.Contains(strings.ReplaceAll(data, "\r\n", ""), "\n") {
		t.Fatal("message must use CRLF line endings")
	}
	m, err := mail.ReadMessage(strings.NewReader(data))
	if err != nil {
		t.Fatalf("parse message: %v", err)
	}
	from, err := m.Header.AddressList("From")
	if err != nil || len(from) != 1 || from[0].Address != testFrom || from[0].Name != "MarsChat" {
		t.Fatalf("From = %v (%v)", from, err)
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject"))
	if err != nil || subject != "MarsChat "+action+"验证码" {
		t.Fatalf("Subject = %q (%v)", subject, err)
	}
	for _, h := range []string{"Date", "Message-ID", "To"} {
		if m.Header.Get(h) == "" {
			t.Errorf("missing header %s", h)
		}
	}
	if _, err := m.Header.Date(); err != nil {
		t.Errorf("bad Date header: %v", err)
	}
	raw, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, m.Body))
	if err != nil {
		t.Fatalf("decode body: %v", err)
	}
	body := string(raw)
	for _, want := range []string{action + "验证码是 " + code, strconv.Itoa(minutes) + " 分钟内有效"} {
		if !strings.Contains(body, want) {
			t.Errorf("body %q missing %q", body, want)
		}
	}
}
