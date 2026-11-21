# 设计：media 服务（重构步骤 6）

- 状态：已采纳（2026-11-20 开发者确认：存储改为可切换的本地目录与阿里云 OSS，默认本地目录；聊天文件上限 100 MB；其余按第 9 节的推荐方案） | 日期：2026-11-20 | 关联：[重构计划步骤 6](../plans/active/backend-refactor.md#6-media-服务)、[ADR-0007](../architecture/adr/0007-object-storage.md)、[ADR-0005](../architecture/adr/0005-distributed-architecture.md)、[网关与多实例设计](gateway-multi-instance.md)、[消息链路设计](message-pipeline.md)、旧基线 [chat-message.md](../product/modules/chat-message.md)、[lessons/static-path-normalization.md](../lessons/static-path-normalization.md)

## 1. 背景与目标

步骤 6 要提供文件能力：聊天中发送文件和图片、用户与群的头像。重构计划写的是“MinIO 存储；上传时由服务端生成文件 ID 并限制大小和类型；下载前校验权限；文件消息引用文件 ID；图片生成缩略图”。

旧基线的做法与问题（提交 `6624cea` 之前）：

- 文件经 `/message/uploadFile` 上传到本机 `static/` 目录，消息里保存完整 URL，换服务器地址后历史消息里的文件全部失效（TD-10，见 lesson）。
- 没有大小和类型限制，下载不校验权限，图片按普通文件展示，没有缩略图。

设计时核实（2026-11-20）发现 **MinIO 已经不能按计划使用**：社区版 2025 年 10 月起只发布源码，2026 年 4 月仓库归档，2026 年 9 月 Docker Hub 删除了 `minio/minio` 镜像。比较了 SeaweedFS、RustFS、Garage 等自建方案后，开发者决定：存储做成可切换的，默认存到仓库中的本地目录，正式环境使用阿里云 OSS。选型记录见 [ADR-0007](../architecture/adr/0007-object-storage.md)。

目标：

- 新增 media 服务（HTTP 18050、gRPC 19050，库 `marschat_media`）。
- 存储后端可切换：`local`（默认，仓库下 `data/media/`，被 Git 忽略）与 `oss`（阿里云 OSS），业务代码只依赖存储接口。
- 上传：服务端生成文件 ID，按用途限制大小，按文件内容（而不是扩展名或客户端声明）识别类型，图片生成缩略图。
- 下载：登录用户凭文件 ID 加访问密钥换取短期有效的签名 URL，浏览器的 `<img>` 和下载链接可以直接使用。
- 消息：新增图片消息与文件消息，消息里只引用文件 ID，元数据由 message 向 media 核实后写入，不信任客户端上送的字段。
- 头像：用户与群的头像字段保存文件 ID（字段已存在，最长 64）。

不做：文件删除与过期清理、视频转码与视频封面、秒传（按哈希去重）、断点续传上传、病毒扫描、管理员封禁文件、两种存储之间的数据迁移。

## 2. 需求与约束

- 资源：Docker Desktop 虚拟机 3.8 GiB，步骤 5 的全部容器约 1.3 GiB。默认的本地存储不新增容器；media 处理图片时需要的内存见 3.6。
- 两种运行模式（见网关设计 2.2）：宿主机模式下服务直接监听 `127.0.0.1`；容器模式下客户端只经网关访问。两种模式下下载 URL 的地址不同，要可配置。
- 浏览器的 `<img>` 不能携带 `Authorization` 头，下载地址必须本身就能鉴权（签名 URL）。
- 接口契约用 protobuf；但上传是二进制流，不适合 protobuf JSON。上传与本地存储的下载用自定义的 HTTP 处理函数，仍经过 Kratos 的中间件链（访问日志、鉴权、参数校验），响应与错误格式和其他接口一致。
- Kratos HTTP 服务器给所有路由套同一个超时（`server.http.timeout`，其他服务 1～3 秒），100 MB 上传需要更长的时间。

## 3. 方案

### 3.1 总体拓扑

```text
上传：客户端 ──> APISIX /v1/files ──> media ──> 存储（local：data/media/；oss：PutObject）
                                         └─ MySQL（files 表）、Redis（限流、缓存、发号、吊销名单）
下载：客户端 ──POST /v1/files/download-urls──> media（校验权限，本地计算签名）
      local：客户端 ──GET {media}/v1/files/{id}/content?签名──> media 校验签名后读文件返回
      oss：  客户端 ──GET OSS 预签名 URL──> 阿里云 OSS
发消息：access ──> message ──GetFiles（gRPC）──> media（核实文件 ID 与访问密钥，取元数据）
```

- media 无状态，可以多实例。`local` 存储下多个实例必须共享同一个目录（同一台机器上挂载同一个目录），容器模式默认 1 个实例。
- media 不发事件、不消费 Kafka：文件记录写入后不再变化，其他服务需要时按 ID 查询。

### 3.2 存储接口与两种实现

biz 只依赖：

```go
type Storage interface {
    // Put 写入对象；size 为字节数，contentType 为识别出的类型
    Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
    Delete(ctx context.Context, key string) error
    // SignURL 生成限时下载地址；disposition 与 contentType 决定浏览器内联显示还是下载
    SignURL(ctx context.Context, key string, f *File, variant Variant, ttl time.Duration) (string, time.Time, error)
}
```

对象键：原文件 `files/{file_id}`，缩略图 `thumbs/{file_id}.jpg`。文件 ID 是 snowflake，不含用户输入，键里不会出现路径穿越。

**local（默认）**

- 根目录 `storage.local.dir`，默认 `data/media`（相对于仓库根目录，`make run-media` 在仓库根目录运行；`data/` 加入 `.gitignore`）。容器模式把宿主机的 `./data/media` 挂载到容器的 `/data/media`。
- 写入：先写根目录下 `tmp/` 中的临时文件（同一文件系统），`fsync` 后 `rename` 到目标路径，读者不会看到写了一半的文件，失败时删除临时文件。为避免单个目录文件过多，按文件名的 FNV-32a 哈希的低 8 位分到 256 个子目录（`files/3f/{id}`）；不用 ID 的低位，因为低流量时 snowflake 的序号位多为 0，会集中到同一个目录。启动时试写一个临时文件，目录不可写（例如容器以非 root 运行、挂载目录无权限）时启动失败。
- 下载：media 自己提供 `GET /v1/files/{id}/content?variant=&expires=&signature=`，`signature` 是用 `storage.local.url_secret` 对 `v1.{文件 ID}.{变体}.{过期时间}` 计算的 HMAC-SHA256（base64url，43 个字符）。media 校验签名（常量时间比较）和过期时间后，按文件记录设置响应头，用 `http.ServeContent` 返回文件，支持 `Range` 与条件请求；`Cache-Control: private, max-age=<地址剩余有效期>`。这个接口不需要访问令牌：签名本身就是授权。
- 下载地址的前缀 `storage.local.public_base_url`：宿主机模式 `http://127.0.0.1:18050`，容器模式 `https://127.0.0.1:19443`（经网关）。
- `url_secret` 必填，多个实例必须相同；`make keys` 生成并写入 `.env`。

**oss（阿里云 OSS）**

- SDK：官方 `github.com/aliyun/alibabacloud-oss-go-sdk-v2`，静态 AccessKey（RAM 用户，只授予该 Bucket 的读写权限），签名 V4。
- 配置：`region`（如 `cn-hangzhou`）、`bucket`、`access_key_id`、`access_key_secret`；可选 `endpoint`（部署在同地域 ECS 时可用内网地址）、`key_prefix`（默认 `marschat/`，便于与其他数据共用 Bucket）、`public_endpoint`（下载地址使用的自定义域名）。
- 写入：`PutObject`，传入读取器与长度，Bucket 保持私有。
- 下载：`Presign(GetObjectRequest)` 本地计算签名，不访问网络；通过 `ResponseContentDisposition`、`ResponseContentType` 覆盖响应头。
- 已知限制：2019 年 9 月 23 日之后创建的 Bucket，用默认域名（`<bucket>.oss-<region>.aliyuncs.com`）直接打开图片或 HTML 时，OSS 强制加上 `Content-Disposition: attachment`。`<img>` 嵌入一般不受影响（实施时验证）；要在浏览器标签页中直接预览，需要为 Bucket 绑定已备案的自定义域名并配置 `public_endpoint`。来源见第 5 节。
- 启动时只校验配置是否完整，不访问 OSS：网络暂时不通不应阻止服务启动，与 SMTP 发送方一致。上传时 OSS 不可用返回 `503`。

切换存储不会迁移已有文件：两种存储的文件互不可见，切换前的文件在新存储中找不到，签发下载地址时按不存在省略。

### 3.3 上传

`POST /v1/files?purpose=PURPOSE_CHAT&name=<URL 编码的文件名>`，请求体是文件的原始字节（`Content-Type: application/octet-stream`），需要 `Authorization: Bearer <访问令牌>`。浏览器可以直接把 `File` 对象作为 `fetch` 的请求体。

流程：

1. 鉴权（Kratos 中间件链中的 `pkg/auth`，含吊销检查）；查询参数按 protovalidate 校验；按用户限流（默认每小时 60 次，Redis 滑动窗口，Redis 故障时放行）。
2. 必须带 `Content-Length`，否则返回 `411 LENGTH_REQUIRED`；长度为 0 返回 `400 VALIDATOR`；超过该用途的上限直接返回 `413 FILE_TOO_LARGE`，不读请求体。读取时再用 `io.LimitReader` 兜底，实际字节数少于 `Content-Length`（客户端中断）或读取超时返回 `400 UPLOAD_INCOMPLETE`，不留下对象和记录。读取请求体不感知请求的 context，处理函数把服务器超时同时设为连接的读取截止时间（`http.ResponseController.SetReadDeadline`），停滞的上传到点后读取失败。
3. 读取前 512 字节，用 `http.DetectContentType` 识别类型：`image/jpeg`、`image/png`、`image/gif`、`image/webp` 视为图片候选，其他按普通文件。扩展名和客户端声明的类型都不参与判断。头像不是图片候选时直接返回 `415`，不再读取其余部分。
4. 图片候选且不超过图片处理上限（10 MiB）时整体读入内存（读入前按 `Content-Length` 预留内存预算，所有请求合计 64 MiB，等待超过 5 秒返回 `503`，见 3.6），先 `image.DecodeConfig` 检查尺寸（不超过 1600 万像素，防止解压炸弹），再解码生成缩略图（3.6）。解码失败或超出上限时按普通文件保存，不报错。
5. 其他文件边读边写入存储，同时计算 SHA-256。
6. 写入成功后插入 `files` 记录；插入失败时尽力删除刚写入的对象。进程在两步之间退出会残留孤儿对象，记为 TD-37，留到做文件清理时处理。
7. 返回 `FileInfo`（4.2）。

用途与限制（`policy`，可热更新）：

| 用途 | 大小上限 | 类型 | 下载权限 |
|---|---|---|---|
| `PURPOSE_CHAT` 聊天文件 | 100 MiB；按图片处理的上限 10 MiB | 任意 | 访问密钥（3.4） |
| `PURPOSE_AVATAR` 头像 | 5 MiB | 必须是可解码的图片，否则 `415 UNSUPPORTED_FILE_TYPE` | 所有登录用户 |

文件名：去掉路径部分、控制字符和首尾空白，截断到 255 字节（按 UTF-8 字符边界），为空时用 `file`。文件名只用于展示和下载时的 `Content-Disposition`，不参与存储路径。

超时：media 的 `server.http.timeout` 设为 300 秒，覆盖 100 MB 在慢速网络下的上传（OSS 模式下 media 边收边写，总耗时约为两段中较慢的一段）；其他接口都很快。APISIX 的 media 上游读写超时同样设为 300 秒，请求体大小上限按 101 MiB 设置。

### 3.4 下载与权限

`POST /v1/files/download-urls`，一次最多 100 项，每项是文件 ID、访问密钥和变体（原文件或缩略图）。media 对每一项：

1. 查文件记录（经缓存，3.7）；不存在或校验不通过的项直接省略，不区分原因，不让客户端借此探测文件 ID。
2. 权限：头像文件所有登录用户都可以读；聊天文件需要访问密钥匹配（常量时间比较），或者请求者就是上传者。
3. 生成签名下载地址（有效期默认 10 分钟），本地计算，不访问存储：
   - 可以内联显示的光栅图片（jpeg、png、gif、webp）及所有缩略图：`Content-Disposition: inline`，`Content-Type` 为识别出的类型；
   - 其他文件（包括 SVG、HTML）：`Content-Disposition: attachment`，`Content-Type: application/octet-stream`，浏览器只会下载，不会当作页面执行，避免存储型 XSS；`local` 下另加 `X-Content-Type-Options: nosniff`。
   - 文件名按 RFC 6266 / RFC 5987 编码（`filename*=UTF-8''...`）。
4. 返回地址与过期时间，客户端在过期前复用。

访问密钥（`access_key`）是上传时生成的 128 位随机数，随 `FileInfo` 返回给上传者，再随消息内容发给会话成员，与 Telegram 的 `access_hash` 同一思路：

- 谁能看到消息，谁就能下载文件；转发消息时密钥随消息一起转发，不需要额外授权。
- 被移出群的成员仍能下载以前见过的文件：他们本来就已经拿到了消息内容。
- 密钥不能撤销；泄露后仍需要登录才能换到下载地址，地址 10 分钟后失效。日志里不打印访问密钥和签名地址。

头像：account 的 `UpdateMe`、social 的建群与修改群资料继续只保存文件 ID，**不调用 media 校验**。头像能否显示由下载时的规则决定：只有用途为头像的文件对所有人公开，把别人的聊天文件 ID 填成头像也换不到下载地址。这样 account、social 不新增对 media 的依赖。

### 3.5 自定义 HTTP 处理函数

上传与本地下载不是 proto 中的 RPC，但仍经过 Kratos 的中间件链：处理函数里用 `ctx.Middleware(...)` 包住实际逻辑，与生成代码的写法相同。

- 访问日志、panic 恢复、错误脱敏、鉴权、参数校验都与其他接口一致；操作名为 `/media.v1.MediaService/UploadFile`、`/media.v1.MediaService/DownloadContent`（后者在鉴权白名单中，由签名授权）。
- 查询参数绑定到 proto 消息（`UploadFileRequest`、`DownloadContentRequest`），protovalidate 照常生效。
- 上传成功返回 `FileInfo` 的 JSON（`pkg/jsoncodec`）；下载直接写响应体；错误按 Kratos 错误格式返回。
- 这两个接口不在 OpenAPI 文档中，在模块文档与 deployment 中手写说明。

### 3.6 缩略图

上传请求内同步生成：

- 解码：标准库 `image/jpeg`、`image/png`、`image/gif`（取第一帧），WebP 用 `golang.org/x/image/webp`。
- 缩放：最长边缩到 320 像素（小于 320 时保持原尺寸），`golang.org/x/image/draw` 的 CatmullRom；输出 JPEG（质量 80），透明背景填白色。
- 资源（2026-11-21 按实测调整，见第 10 节）：同一实例同时只做 1 个解码任务，等待超过 5 秒返回 `503`。一个解码任务的存活内存 = 位图（8 位每像素 4 字节，16 位 PNG 每像素 8 字节）+ CatmullRom 缩放的中间缓冲（目标宽 × 原图高 × 32 字节），1600 万像素的 16 位 PNG 约 128 + 41 MB。等待解码的图片整体读入内存，所有请求合计不超过 64 MiB（按 `Content-Length` 预留，等待超过 5 秒返回 `503`）。存活内存最坏约 64 + 169 MB 加进程基线，media 容器的内存限制为 384 MiB，并设 `GOMEMLIMIT=320MiB`，让 GC 在接近上限前回收，而不是被 OOM 杀掉。
- 已知限制：不处理 JPEG 的 EXIF 方向，部分手机照片的缩略图会是横着的，记为 TD-38。
- 记录原图宽高与缩略图大小，供客户端排版。

### 3.7 元数据缓存

文件记录写入后不再变化，适合缓存：下载地址接口和发消息时的核实都按 ID 批量读取，经 `pkg/cache`（Cache-Aside，空值标记防穿透，TTL 30 分钟加抖动）。记录不会被修改，不存在缓存与数据库不一致的问题；不加布隆过滤器（ID 不对外列举，空值标记足够）。

### 3.8 图片消息与文件消息

`MessageContent` 新增两种类型（4.3）。客户端发消息时只需要填文件 ID 和访问密钥，message 处理：

1. 调 media 内部接口 `GetFiles`（经 Consul，超时 1 秒，只读接口可以重试，带熔断，与调用 social、account 相同）。
2. 校验：文件存在、访问密钥匹配、用途是聊天；图片消息还要求文件是带缩略图的图片。任一不满足返回 `400 FILE_INVALID`，不区分原因。
3. 用 media 返回的名称、大小、类型、宽高覆盖客户端上送的值，再落库。旧基线的 lesson 说明过：展示字段不能信任客户端。
4. media 不可用时返回 `503 DEPENDENCY_UNAVAILABLE`；文本消息不调用 media，不受影响。

接收方从消息内容拿到文件 ID 和访问密钥，调下载地址接口取缩略图和原文件。

## 4. 数据结构与接口

### 4.1 表（库 `marschat_media`，迁移 `000001_init`）

**files**

| 列 | 类型 | 说明 |
|---|---|---|
| `id` | `BIGINT` PK | snowflake，即文件 ID |
| `uploader_id` | `BIGINT` | 上传者 |
| `purpose` | `TINYINT` | 1 聊天、2 头像 |
| `kind` | `TINYINT` | 1 普通文件、2 图片（已生成缩略图） |
| `name` | `VARCHAR(255)` | 清理后的文件名 |
| `size` | `BIGINT` | 字节数 |
| `mime_type` | `VARCHAR(127)` | 按内容识别的类型 |
| `sha256` | `BINARY(32)` | 完整性校验，将来做秒传时可用 |
| `width` / `height` | `INT` | 图片原始宽高，普通文件为 0 |
| `thumbnail_size` | `INT` | 缩略图字节数，没有缩略图为 0 |
| `access_key` | `BINARY(16)` | 访问密钥，接口中按 base64url 编码为 22 个字符 |
| `storage` | `TINYINT` | 写入时的存储：1 local、2 oss，用于排查切换存储后的问题 |
| `created_at` | `DATETIME(3)` | |

查询只有按主键（单个与 `IN` 批量），不需要二级索引。media 不发事件，不建 `outbox_events` 表。

### 4.2 接口（`api/media/v1/media.proto`、`internal.proto`、`error_reason.proto`）

| 方法 | HTTP | 说明 |
|---|---|---|
| 上传（自定义处理函数） | `POST /v1/files?purpose=&name=` | 请求体为文件字节，返回 `FileInfo` |
| `GetDownloadUrls` | `POST /v1/files/download-urls` | `items`：`fileId`、`accessKey`、`variant`（`VARIANT_ORIGINAL` / `VARIANT_THUMBNAIL`），最多 100 项；返回 `url`、`expiresAt`，校验不通过的项省略 |
| 本地下载（自定义处理函数，仅 `local`） | `GET /v1/files/{file_id}/content?variant=&expires=&signature=` | 校验签名后返回文件，支持 `Range` |
| `MediaInternalService.GetFiles` | 仅 gRPC | 按 ID 批量返回 `FileInfo`（含访问密钥与上传者），供 message 核实，最多 100 个 |

`FileInfo`：`id`、`accessKey`、`purpose`、`kind`、`name`、`size`、`mimeType`、`width`、`height`、`hasThumbnail`、`createdAt`。

错误原因：

| 原因 | 状态码 | 场景 |
|---|---|---|
| `UPLOAD_INCOMPLETE` | 400 | 请求体没有读完（客户端中断、超时、字节数少于 `Content-Length`），实施时新增 |
| `FILE_TOO_LARGE` | 413 | 超过用途的大小上限，metadata 带 `max_bytes` |
| `UNSUPPORTED_FILE_TYPE` | 415 | 头像不是可解码的图片 |
| `LENGTH_REQUIRED` | 411 | 上传请求没有 `Content-Length` |
| `RATE_LIMITED` | 429 | 上传过于频繁，metadata 带 `retry_after_seconds` |
| `DOWNLOAD_URL_INVALID` | 403 | 本地下载地址的签名错误或已过期 |
| `FILE_NOT_FOUND` | 404 | 本地下载时文件不存在（签名正确但存储中没有，例如切换过存储） |
| `DEPENDENCY_UNAVAILABLE` | 503 | 存储或数据库不可用、图片处理排队超时 |

### 4.3 消息内容（`api/message/v1/message.proto`，新增不破坏兼容）

```proto
message MessageContent {
  oneof body {
    TextContent text = 1;
    ImageContent image = 2;
    FileContent file = 3;
  }
}

message ImageContent {
  int64 file_id = 1;      // 发送时必填
  string access_key = 2;  // 发送时必填
  string name = 3;        // 以下由 message 按 media 的记录填写，客户端上送的值被覆盖
  int64 size = 4;
  string mime_type = 5;
  int32 width = 6;
  int32 height = 7;
}

message FileContent {
  int64 file_id = 1;
  string access_key = 2;
  string name = 3;
  int64 size = 4;
  string mime_type = 5;
}
```

`file_id` 与其他 ID 一样用 `int64`（JSON 中为字符串）。`messages.content` 存的是 `MessageContent` 的 protobuf 编码，新增类型不改表。message 新增错误原因 `FILE_INVALID`（400）。

### 4.4 配置（`app/media/configs/config.yaml`）

```yaml
storage:
  provider: ${STORAGE_PROVIDER:local}        # local | oss，切换需要重启
  local:
    dir: ${MEDIA_LOCAL_DIR:data/media}
    public_base_url: ${MEDIA_PUBLIC_BASE_URL:http://127.0.0.1:18050}
    url_secret: ${MEDIA_URL_SECRET:}          # make keys 生成
  oss:
    region: ${OSS_REGION:}
    endpoint: ${OSS_ENDPOINT:}
    bucket: ${OSS_BUCKET:}
    access_key_id: ${OSS_ACCESS_KEY_ID:}
    access_key_secret: ${OSS_ACCESS_KEY_SECRET:}
    key_prefix: ${OSS_KEY_PREFIX:marschat/}
    public_endpoint: ${OSS_PUBLIC_ENDPOINT:}
policy:                                       # 热更新
  max_chat_file_bytes: 104857600
  max_image_bytes: 10485760
  max_avatar_bytes: 5242880
  max_image_pixels: 16000000
  thumbnail_max_side: 320
  download_url_ttl: 600s
  upload_limit: {limit: 60, window: 3600s}
```

### 4.5 端口与部署

- media：HTTP 18050、gRPC 19050（端口表中已预留）；容器模式 1 个实例，注册到 Consul。就绪探针只检查 MySQL 与 Redis，不检查文件存储：OSS 暂时不可达时下载地址照常签发（本地计算），不应让实例被摘除。
- 不新增存储容器；端口表中的 MinIO 行（19000/19001）删除，端口释放。
- `make run-media`、`make migrate-media`；`make images` 包含 media；compose 的 `apps` profile 新增 media，挂载 `./data/media`。
- `.env.example` 新增 `MARSCHAT_MEDIA_URL_SECRET`（`make keys` 生成）以及注释掉的 OSS 变量。

## 5. 候选方案与权衡

### 5.1 存储

开发者决定：存储可切换，默认本地目录，正式环境用阿里云 OSS。设计阶段比较过的方案与结论见 [ADR-0007](../architecture/adr/0007-object-storage.md)。

本地目录作为默认值的好处：开箱即用，不需要云账号，也不新增容器（本机内存紧张）；代价是下载要经过 media，多实例时必须共享目录。OSS 的好处是下载不经过 media、容量与可用性由云服务保证；代价是需要 AccessKey 与费用，开发机经公网上传。

OSS 默认域名强制下载的来源：[阿里云开发者社区：绑定自定义域名解决强制下载](https://developer.aliyun.com/article/1431818)、[OSS 资源被强制下载的解决方法](https://developer.aliyun.com/ask/390888)。

### 5.2 上传路径

| 方案 | 优点 | 缺点 | 结论 |
|---|---|---|---|
| A. 经 media 中转 | 大小在读取时就能限制；按内容识别类型、生成缩略图都在同一个请求里完成；上传成功即有记录，没有“只申请未上传”的中间状态；两种存储用同一条路径 | 上传流量经过网关和 media | 采用 |
| B. 预签名直传 OSS | media 不在上传通路上 | 只适用于 OSS；大小要靠 POST 策略限制；类型识别和缩略图要在上传后再读一遍对象；需要“申请—上传—确认”三步和对放弃上传的清理 | 文件上限提高到数百 MB 或要支持断点续传时再考虑 |

### 5.3 下载权限

| 方案 | 优点 | 缺点 | 结论 |
|---|---|---|---|
| A. 访问密钥加签名 URL | 权限校验不依赖其他服务；签名 URL 可以直接用于 `<img>` | 密钥不能撤销；被移出群的成员仍能下载以前见过的文件 | 采用 |
| B. 按会话成员校验 | 权限最精确 | media 要记录文件属于哪些会话（发消息时回写），每次下载都要问 message 当前用户是否是成员；转发要追加关联 | 需要撤销访问时再考虑 |

### 5.4 缩略图

| 方案 | 优点 | 缺点 | 结论 |
|---|---|---|---|
| A. 上传请求内同步生成 | 上传返回时缩略图就绪，图片消息发出去就能显示；没有中间状态 | 上传延迟增加（实测）；解码占内存，要限制并发和像素数 | 采用 |
| B. 经 Outbox 和 Kafka 异步生成（复用重试队列与死信队列） | 上传更快；失败可以重试 | 发消息时缩略图可能还没好，客户端要处理“生成中”；要新增事件、消费者和状态字段 | 处理视频封面等耗时任务时再引入 |
| C. OSS 图片处理（`x-oss-process` 实时缩放） | 不用自己解码 | 只适用于 OSS，本地存储仍要自己生成；按处理量计费 | 不采用 |

## 6. 新增依赖与环境变化

- Go 依赖（修改 `go.mod`，开发者已同意）：`github.com/aliyun/alibabacloud-oss-go-sdk-v2`（OSS 官方 SDK，Apache-2.0）、`golang.org/x/image`（缩放与 WebP 解码，BSD）。
- 不新增镜像与依赖容器。
- Compose：`apps` profile 新增 media，挂载 `./data/media`；APISIX 新增 media 路由。
- `.env`：新增 `MARSCHAT_MEDIA_URL_SECRET`；使用 OSS 时再填 `MARSCHAT_STORAGE_PROVIDER=oss` 与 `MARSCHAT_OSS_*`。
- `.gitignore`：新增 `data/`。

## 7. 风险与验证

| 风险 | 应对 |
|---|---|
| 容器以非 root 用户运行，写不进挂载的宿主机目录 | 2026-11-21 在 Docker Desktop（Hyper-V 后端）上验证：nonroot 用户可以写入挂载的 `data/media`，启动试写通过，不需要在 compose 中指定用户。Linux 宿主机上目录属主不同时可能需要 `chown` 或指定用户 |
| 内存不足 | 图片解码限制像素数和并发，读入内存的图片限制总字节数。2026-11-21 实测（容器模式经网关并发上传，读 media 进程的 VmHWM）：原设计的 2 个并发解码在 1440 万像素 8 位 PNG 下峰值已达 330 MiB，按此调整（第 10 节）；调整后同样负载峰值 215 MiB，最坏情况（1590 万像素 16 位 PNG，4 个并发）335 MiB，在 384 MiB 限制内 |
| 上传与数据库写入之间进程退出留下孤儿对象 | 记为技术债；影响只是占用空间，不影响正确性 |
| 访问密钥泄露 | 仍需登录才能换到下载地址，地址 10 分钟有效；需要撤销时改用 5.3 的方案 B |
| 恶意文件当作页面执行 | 非图片一律以 `attachment` 和 `application/octet-stream` 下载；SVG 不内联 |
| OSS 默认域名直接打开图片时被强制下载 | `<img>` 嵌入预计不受影响，在 OSS 手动验证中确认（尚未执行）；需要预览时绑定自定义域名并配置 `public_endpoint` |
| OSS 凭据泄露 | 使用只授权单个 Bucket 的 RAM 用户 AccessKey，只放在 `.env`；日志不打印配置 |
| 经网关上传的大请求被缓冲到 APISIX 磁盘 | 100 MB 可以接受；必要时对 media 路由关闭请求缓冲。是否落盘尚未核实；集群测试中 20 MiB 经网关上传约 0.6 秒、下载约 0.14 秒 |

验证：

- 单元测试：大小与类型规则、文件名清理、访问密钥比较、头像公开规则、缩略图生成（测试内生成 PNG、JPEG、GIF、WebP）、超像素与损坏图片按普通文件处理、`Content-Disposition` 的编码与 inline/attachment 选择、本地签名的生成与校验（篡改、过期）、本地存储的原子写入、OSS 预签名地址的参数（离线计算）与 `PutObject` 请求（对接测试内的假服务器）、message 对文件内容的核实与字段覆盖。
- e2e（宿主机模式，本地存储）：上传普通文件与图片；用下载地址取回原文件与缩略图，比对字节和响应头；`Range` 请求；篡改签名或过期返回 `403`；错误密钥的项被省略；头像对其他用户可读；超限 `413`；头像传非图片 `415`；发送图片消息与文件消息，客户端伪造的元数据被覆盖；伪造文件 ID 返回 `FILE_INVALID`。
- 集群测试（容器模式，只经网关）：经网关上传与下载。
- OSS 手动验证：开发者配置 Bucket 与 AccessKey 后，上传、经预签名地址下载、`<img>` 显示，记录耗时。
- `EXPLAIN` 核对主键批量查询；记录上传 100 MB 与生成缩略图的耗时、media 内存。

## 8. 实施顺序

1. media 骨架：proto、配置、迁移、wire、Consul 注册、镜像与 Compose、Makefile；验证容器内挂载目录的写权限。
2. 存储接口与 local、oss 两种实现，单元测试。
3. 上传、缩略图、下载地址、本地下载、内部 `GetFiles`、元数据缓存，单元测试与 e2e。
4. message 接入图片与文件消息，e2e。
5. 网关路由、集群测试、文档（features、chat-message 与 user-auth 模块文档、data-model、data-flow、system、deployment、端口表、ADR-0005/0007）。
6. OSS 手动验证（需要开发者的 Bucket 与 AccessKey）。

## 9. 确认结果（2026-11-20）

1. 对象存储：设计推荐 SeaweedFS；开发者改为可切换的存储，默认仓库中的本地目录，并实现阿里云 OSS 集成（ADR-0007）。
2. 传输路径：上传经 media 中转；下载用签名 URL（local 由 media 签名并提供下载，oss 用预签名地址）。
3. 下载权限：按推荐，访问密钥模式；头像对登录用户公开，account、social 不调用 media 校验。
4. 缩略图：按推荐，上传时同步生成。
5. 限额：聊天文件上限改为 100 MB；其余按推荐（按图片处理的上限 10 MiB 与 1600 万像素，头像 5 MiB，每人每小时上传 60 次）。
6. 新增依赖：OSS 官方 SDK v2 与 `golang.org/x/image`；不再需要 `minio-go` 和存储镜像。

## 10. 实施中的调整

1. 缩略图并发与内存预算（2026-11-21）：3.6 原来按“两个并发、每个约 64 MB”估算，漏了 CatmullRom 缩放的中间缓冲（目标宽 × 原图高 × 32 字节，1600 万像素约 41 MB），16 位 PNG 的位图也按 64 MB 算少了一半。实测两个并发解码 1440 万像素的 8 位 PNG，media 进程峰值常驻内存 330 MiB，已超过 `GOMEMLIMIT`；按估算，两个 1600 万像素的 16 位 PNG 同时解码，存活内存约 340 MB，GC 也回收不掉，可能被 OOM 杀掉。另外，图片在排队等待解码前就整体读入内存，并发上传多时这部分没有上限。调整为：同时只解码 1 张；读入内存的图片按 `Content-Length` 预留预算，合计 64 MiB（`golang.org/x/sync/semaphore`，`x/sync` 原本就是直接依赖），超过预算的单个请求按预算计、独占执行。代价是多张大图并发上传时排队：8 张 1440 万像素的图片总耗时从 1.0 秒变为 1.75 秒。
