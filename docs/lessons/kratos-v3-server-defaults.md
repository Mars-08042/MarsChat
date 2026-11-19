# Kratos v3 服务端的默认组件不能直接用：日志泄露敏感数据、HTTP 不按 protobuf JSON 收发

- 涉及：[pkg/middleware/accesslog/accesslog.go](../../pkg/middleware/accesslog/accesslog.go)、[pkg/jsoncodec/jsoncodec.go](../../pkg/jsoncodec/jsoncodec.go)、[pkg/bootstrap/encoding.go](../../pkg/bootstrap/encoding.go)、[app/account/internal/server/server.go](../../app/account/internal/server/server.go)；Kratos 源码 `github.com/go-kratos/kratos/v3@v3.0.0` 的 `middleware/logging/logging.go`、`middleware/recovery/recovery.go`、`errors/errors.go`、`encoding/json/json.go`、`transport/http/codec.go`
- 发现时间：2026-11-18，重构步骤 2 实现 account 服务时

两个问题的共同点：照着 Kratos 示例直接挂上默认的中间件和编码，功能上都能跑通，问题只在特定输入或审查日志时才暴露。

## 一、日志中间件把密码、验证码、刷新令牌写进日志

### 现象

写 account 的 server 层时读了 Kratos 自带中间件的源码，发现问题后用临时测试复现（登录请求带密码 `secret-pass-123`）：

```
# logging.Server：一个普通的 401 业务错误
level=ERROR msg="server request" operation=/account.v1.AccountService/LoginByPassword args="phone:\"13800000001\"  password:\"secret-pass-123\"" code=401 reason=INVALID_CREDENTIALS ...

# recovery.Recovery：handler panic
level=ERROR msg="panic recovered" panic=boom request="phone:\"13800000001\"  password:\"secret-pass-123\"" stack=...
```

同时发现第三个问题：handler 返回的不是 Kratos 错误时（例如数据库报错），客户端收到的是：

```
code=500 reason="" message="Error 1062 (23000): Duplicate entry '13800000001' for key 'users.uk_phone'"
```

### 根因

- `logging.Server` 用 `extractArgs(req)` 把请求写进 `args` 字段：请求实现了 `Redacter` 接口就调用 `Redact()`，否则调用 `String()`。生成的 proto 消息都有 `String()`，于是整个请求体按文本格式输出。注册、登录、刷新令牌的请求里有密码、验证码和刷新令牌，全部落盘。
- `logging.Server` 的 `extractError` 对任何非 nil 错误都返回 ERROR 级别，“密码错误”这类正常的业务分支也记成 ERROR，按日志级别告警会被淹没。
- `recovery.Recovery` 记录 panic 时带上 `slog.Any("request", req)`，同样输出整个请求。
- `errors.FromError` 遇到非 Kratos 错误时返回 `New(UnknownCode, UnknownReason, err.Error())`，HTTP 的默认错误编码器把这个 message 原样写给客户端，泄露了表结构、索引名和用户数据。

可以给每个敏感请求实现 `Redact()`，但这是“默认泄露、逐个补救”：新增接口时忘了写，就会再次泄露。

### 解决

新增 `pkg/middleware/accesslog`，替代 `logging` 和 `recovery`，放在中间件链最外层（HTTP 与 gRPC 共用）：

- 访问日志只记操作名、状态码、错误原因和耗时，不记录请求体和响应体。
- 状态码 5xx 记 ERROR，其余记 INFO。
- panic 恢复后只记 panic 值和堆栈，不记请求。
- 非 Kratos 错误先把原始错误写进日志，再换成 `500 INTERNAL`、message 为“服务器内部错误”返回给客户端。业务错误原样返回。

### 验证

`pkg/middleware/accesslog` 的单元测试用带密码的登录请求覆盖四种情况：成功、业务错误、内部错误、panic。断言日志里没有密码和手机号，内部错误不会返回给客户端，原始错误写进了日志。

## 二、HTTP 接口不按 protobuf JSON 规则收发

### 现象

按 protobuf JSON 格式（与 OpenAPI 文档一致）调用 `POST /v1/auth/sms-codes`：

```json
{"phone":"13800000001","purpose":"SMS_PURPOSE_REGISTER"}
```

返回 400：

```
{"code":400,"reason":"CODEC","message":"body unmarshal json: cannot unmarshal string into Go struct field SendSmsCodeRequest.purpose of type v1.SmsPurpose"}
```

枚举改成数字后请求能通过，但响应里的 int64 ID 是数字而不是字符串，前端会丢精度。`google.protobuf.Timestamp` 被编码成 `{"seconds":…,"nanos":…}`，字段名是 proto 原名（`sms_code`）而不是 `smsCode`。

还有一个容易混淆的现象：curl 的 `-d` 默认带 `Content-Type: application/x-www-form-urlencoded`，Kratos 会选 form codec，把请求体当成表单解析，所有字段为空，参数校验报“全部字段不合法”。调试时要加 `-H 'Content-Type: application/json'`。

### 根因

Kratos v2 的 `encoding/json` 遇到 `proto.Message` 时用 protojson 编解码。v3 把它拆成了两个 codec（见 Kratos 仓库的 `docs/migration/v2-to-v3_zh.md`）：

- `encoding/json` 注册名为 `json` 的纯标准库 codec；
- `encoding/protojson` 注册名为 `protojson` 的 codec。

HTTP 服务器按 `Content-Type` 的子类型选 codec。`application/json` 选中的是纯标准库的 `json`，于是 proto 消息按 Go 结构体的 json 标签处理。只有客户端发送 `application/protojson` 时才会用 protojson，浏览器和 API 工具一般不这样写。

### 解决

新增 `pkg/jsoncodec`，用同名 `json` 覆盖注册：proto 消息用 protojson（输出零值字段、忽略未知字段），其他值仍用标准库。`pkg/bootstrap` 匿名导入它，所有服务自动生效。

覆盖依赖于注册顺序：`encoding.RegisterCodec` 按名字存进 map，后注册的覆盖先注册的。`pkg/jsoncodec` 导入了 Kratos 的 `encoding/json`，被导入的包一定先初始化，所以本包的注册一定在后面。

Kratos 提供了 contrib 兼容包 `contrib/encoding/json/v3`，行为相同，但标注为迁移期兼容用途。为几十行代码引入一个伪版本依赖不划算，所以自己实现。

### 验证

- `pkg/jsoncodec` 的单元测试覆盖：
  - 枚举字符串可以解析，未知字段被忽略；
  - int64 输出为字符串，Timestamp 输出为 RFC 3339，零值字段也会输出；
  - Kratos 错误和普通值不受影响。
- e2e 用 protojson 编码请求、解码响应，全部接口通过。

## 预防

- 不使用 Kratos 自带的 `logging`、`recovery` 中间件；新服务的中间件链照 account 的 `server.go` 组装，`accesslog` 放在最外层。
- 任何会写日志的地方都不要整体打印请求、响应或配置对象，只挑需要的字段；手机号用 `biz.MaskPhone` 脱敏。
- 新服务都通过 `pkg/bootstrap` 启动，不要绕过它直接创建 HTTP 服务器，否则 `pkg/jsoncodec` 不会生效。
- 升级 Kratos 时，检查 `encoding/json`、`logging`、`recovery`、`errors.FromError` 的行为是否有变化。`pkg/jsoncodec` 和 `pkg/middleware/accesslog` 的测试会先失败。
