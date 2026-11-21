package data

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"

	"github.com/Mars-08042/MarsChat/app/account/internal/biz"
	"github.com/Mars-08042/MarsChat/app/account/internal/conf"
)

// defaultSMTPTimeout 是未配置 mail.smtp.timeout 时一次发送的最长时间，留出余量给 3 秒的请求超时。
const defaultSMTPTimeout = 2500 * time.Millisecond

// smtpSender 经 SMTP 服务器发送验证码邮件。
//
// 每次发送新建连接：验证码邮件频率低，保持长连接还要处理服务器的空闲断开，得不偿失。
// 465 端口使用隐式 TLS，其他端口要求 STARTTLS；不支持明文发送，授权码不能经明文连接传输。
type smtpSender struct {
	addr        string
	host        string
	username    string
	password    string
	timeout     time.Duration
	implicitTLS bool
	tlsConfig   *tls.Config
	logger      *slog.Logger
	now         func() time.Time
}

func newSMTPSender(c *conf.Smtp, logger *slog.Logger) (*smtpSender, error) {
	host, port, err := net.SplitHostPort(c.GetAddr())
	if err != nil || host == "" || port == "" {
		return nil, fmt.Errorf("mail.smtp.addr must be host:port, got %q", c.GetAddr())
	}
	if c.GetUsername() == "" || c.GetPassword() == "" {
		return nil, errors.New("mail.smtp.username and mail.smtp.password are required by the smtp provider")
	}
	// 用户名同时是发件人地址
	if a, err := mail.ParseAddress(c.GetUsername()); err != nil || a.Address != c.GetUsername() {
		return nil, fmt.Errorf("mail.smtp.username must be a bare email address, got %q", c.GetUsername())
	}
	timeout := c.GetTimeout().AsDuration()
	if timeout <= 0 {
		timeout = defaultSMTPTimeout
	}
	return &smtpSender{
		addr: c.GetAddr(), host: host, username: c.GetUsername(), password: c.GetPassword(),
		timeout: timeout, implicitTLS: port == "465",
		tlsConfig: &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12},
		logger:    logger, now: time.Now,
	}, nil
}

func (s *smtpSender) Send(ctx context.Context, to, code string, purpose int32, ttl time.Duration) error {
	// 收件地址来自已校验的请求，这里再挡一次换行，杜绝邮件头注入
	if strings.ContainsAny(to, "\r\n") {
		return fmt.Errorf("invalid recipient %q", biz.MaskEmail(to))
	}
	if reservedDomain(to) {
		return fmt.Errorf("recipient %s uses a reserved domain that has no real mailbox", biz.MaskEmail(to))
	}
	msg, err := buildCodeMail(s.username, to, code, purpose, ttl, s.now())
	if err != nil {
		return err
	}

	deadline := s.now().Add(s.timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	begin := s.now()
	conn, err := s.dial(ctx, deadline)
	if err != nil {
		return fmt.Errorf("smtp dial %s: %w", s.addr, err)
	}
	defer conn.Close()
	// 截止时间覆盖整个会话；调用方取消时立即关闭连接，让阻塞的读写返回
	if err := conn.SetDeadline(deadline); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	if err := s.deliver(conn, to, msg); err != nil {
		return err
	}
	s.logger.Debug("verification mail sent", slog.String("email", biz.MaskEmail(to)), slog.Duration("took", s.now().Sub(begin)))
	return nil
}

func (s *smtpSender) dial(ctx context.Context, deadline time.Time) (net.Conn, error) {
	d := &net.Dialer{Deadline: deadline}
	if s.implicitTLS {
		td := &tls.Dialer{NetDialer: d, Config: s.tlsConfig.Clone()}
		return td.DialContext(ctx, "tcp", s.addr)
	}
	return d.DialContext(ctx, "tcp", s.addr)
}

func (s *smtpSender) deliver(conn net.Conn, to string, msg []byte) error {
	c, err := smtp.NewClient(conn, s.host)
	if err != nil {
		return fmt.Errorf("smtp greeting: %w", err)
	}
	defer c.Close()
	if !s.implicitTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("smtp server does not support STARTTLS, refusing to send credentials in plain text")
		}
		if err := c.StartTLS(s.tlsConfig.Clone()); err != nil {
			return fmt.Errorf("smtp starttls: %w", err)
		}
	}
	if err := c.Auth(smtp.PlainAuth("", s.username, s.password, s.host)); err != nil {
		return fmt.Errorf("smtp auth: %w", err)
	}
	if err := c.Mail(s.username); err != nil {
		return fmt.Errorf("smtp mail from: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("smtp rcpt to: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("smtp write message: %w", err)
	}
	// 关闭 DATA 时服务器返回是否接受这封邮件，之后的 QUIT 失败不影响投递
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp end data: %w", err)
	}
	if err := c.Quit(); err != nil {
		s.logger.Debug("smtp quit failed after the message was accepted", slog.Any("error", err))
	}
	return nil
}

// reservedDomain 判断收件地址是否属于不会有真实邮箱的保留域名（RFC 2606、RFC 6761）。
// e2e 的测试账号（example.test）和迁移填充的占位邮箱（legacy.invalid）都在其中，
// 误开 smtp 发送方跑测试时直接拒绝，不让退信占用个人邮箱的每日发信额度。
func reservedDomain(email string) bool {
	d := email[strings.LastIndexByte(email, '@')+1:]
	for _, tld := range []string{"test", "example", "invalid", "localhost"} {
		if d == tld || strings.HasSuffix(d, "."+tld) {
			return true
		}
	}
	switch d {
	case "example.com", "example.net", "example.org":
		return true
	}
	return false
}

// buildCodeMail 生成纯文本的验证码邮件，行尾为 CRLF，正文按 base64 编码。
func buildCodeMail(from, to, code string, purpose int32, ttl time.Duration, now time.Time) ([]byte, error) {
	action := "登录"
	if purpose == biz.PurposeRegister {
		action = "注册"
	}
	minutes := max(int(math.Ceil(ttl.Minutes())), 1)
	body := fmt.Sprintf("你的 MarsChat %s验证码是 %s，%d 分钟内有效。\r\n\r\n如果不是你本人操作，请忽略本邮件，不要把验证码告诉任何人。\r\n",
		action, code, minutes)

	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return nil, err
	}
	domain := from[strings.LastIndexByte(from, '@')+1:]

	var b bytes.Buffer
	header := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	header("From", (&mail.Address{Name: "MarsChat", Address: from}).String())
	header("To", (&mail.Address{Address: to}).String())
	header("Subject", mime.BEncoding.Encode("utf-8", "MarsChat "+action+"验证码"))
	header("Date", now.Format(time.RFC1123Z))
	header("Message-ID", "<"+hex.EncodeToString(id)+"@"+domain+">")
	header("MIME-Version", "1.0")
	header("Content-Type", "text/plain; charset=UTF-8")
	header("Content-Transfer-Encoding", "base64")
	b.WriteString("\r\n")
	enc := base64.StdEncoding.EncodeToString([]byte(body))
	for len(enc) > 76 {
		b.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc + "\r\n")
	return b.Bytes(), nil
}
