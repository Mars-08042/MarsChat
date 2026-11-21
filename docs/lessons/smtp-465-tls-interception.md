# 开发机到 465 端口的 TLS 连接被截获，SMTP 发信报证书不受信任

- 涉及：[app/account/internal/data/mail.go](../../app/account/internal/data/mail.go)（`newSMTPSender`、`smtpSender.dial`）；`.env` 的 `MARSCHAT_SMTP_ADDR`
- 发现时间：2026-11-20，邮箱验证码的真实发信验证

## 现象

`.env` 按 QQ 邮箱的常见配置填写 `MARSCHAT_SMTP_ADDR=smtp.qq.com:465`。容器模式下发送注册验证码，接口约 90 毫秒返回 `503 CODE_SEND_FAILED`，account 日志：

```text
level=ERROR msg="send verification code failed" email=t***@<开发者的域名>
error="smtp dial smtp.qq.com:465: tls: failed to verify certificate: x509: certificate signed by unknown authority"
```

运行镜像是 distroless，自带根证书；同一个容器改用 587 端口后发送成功，排除了镜像缺少根证书。

## 根因

在宿主机上用 openssl 查看各端点出示的证书链：

```bash
echo | openssl s_client -connect smtp.qq.com:465 -servername smtp.qq.com
echo | openssl s_client -connect smtp.qq.com:587 -starttls smtp -servername smtp.qq.com
```

| 端点 | 签发者 | 校验结果 |
|---|---|---|
| `smtp.qq.com:465`、`smtp.163.com:465` | `C=CN, ST=BeiJing, L=ChaoYang, O=Public Certificate, CN=Public Certificate`（自签根证书） | `67 (CA certificate key too weak)` |
| `smtp.qq.com:587`（STARTTLS） | DigiCert | `0 (ok)` |
| `mail.qq.com:443`、`www.qq.com:443`、`github.com:443` | DigiCert、GlobalSign、Sectigo | `0 (ok)` |

两家邮箱的 465 端口拿到的是同一张伪造证书，而 HTTPS 与 587 正常：开发机所在的网络路径上有程序专门截获隐式 TLS 的邮件端口，用自己的根证书重新签发。这张根证书不在 Windows 的受信任根证书库中（查过 `Cert:\LocalMachine\Root` 和 `Cert:\CurrentUser\Root`），本机正常的邮件客户端同样会拒绝它。具体是哪个程序或设备在截获未确认（开发机上运行着本地代理，代理的 TUN 模式、安全软件的邮件扫描、网关设备都可能）。

发送方拒绝是正确的：TLS 握手在认证之前失败，授权码没有发出。如果关闭证书校验，授权码会交给截获者。

## 解决

- 改用 587 端口（STARTTLS）：`MARSCHAT_SMTP_ADDR=smtp.qq.com:587`。代码不用改：非 465 端口强制 STARTTLS，服务器不支持时拒绝发送，不会降级为明文。
- `.env.example` 与 [deployment.md](../deployment.md#验证码邮件) 的示例改为 587，并写明这个现象的排查方法。

## 验证

2026-11-20，容器模式经网关发送：注册验证码与登录验证码各一封，从请求到 SMTP 服务器接受邮件分别为 1.53 秒、1.18 秒，邮件正常收到；用它们完成了注册和邮箱验证码登录，同一个验证码第二次使用返回 `400 CODE_INVALID`。

## 预防

- SMTP 报 `certificate signed by unknown authority` 时，先用上面的 openssl 命令看签发者，不要关闭证书校验（`InsecureSkipVerify`）。
- 换一台机器或网络部署时，465 与 587 都可能被截获，以 openssl 的校验结果为准。
