# ADR-0007 文件存储：可切换的本地目录与阿里云 OSS

- 状态：已采纳（2026-11-20 开发者确认）
- 日期：2026-11-20
- 关联：[ADR-0005](0005-distributed-architecture.md) 决策 6（本 ADR 替代其中“MinIO 存文件”）、[media 服务设计](../../design/media-service.md)、[重构计划步骤 6](../../plans/active/backend-refactor.md#6-media-服务)

## 背景

ADR-0005 选定 MinIO 作为文件存储。重构步骤 6 设计时（2026-11-20）核实发现 MinIO 已经不能按计划使用：

- 2025 年 10 月起社区版只发布源码，不再发布二进制与镜像；2026 年 4 月 GitHub 仓库归档。
- 2026 年 9 月 Docker Hub 删除了 `minio/minio` 镜像，Compose 无法再拉取。
- Go 客户端 `minio-go` 仍在维护，但它只是 S3 客户端，不解决服务端从哪里来的问题。

来源：[Wikipedia: MinIO](https://en.wikipedia.org/wiki/MinIO)、[MinIO Community Edition Archived: What to Do](https://www.cloudhim.com/cloud-infrastructure/minio-community-edition-archived-what-to-do)、[MinIO archived: self-hosted S3 alternatives](https://pinggy.io/blog/minio_archived_self_hosted_s3_alternatives/)、[MinIO archived: Garage, SeaweedFS, Ceph RGW](https://ttlab.hopto.org/blog/2026-07-17-minio-archived-garage-seaweedfs-ceph-rgw?lang=en)。

约束：开发机的 Docker Desktop 虚拟机只有 3.8 GiB，步骤 5 的容器已用约 1.3 GiB；项目用于求职展示，开发者希望开箱即用，同时具备接入云存储的能力。

## 候选方案

| 方案 | 说明 | 结论 |
|---|---|---|
| A 自行构建 MinIO 镜像或使用旧镜像 | 从归档的源码构建，或固定某个旧版本 | 不再有安全更新，不采用 |
| B SeaweedFS（`weed mini` 单容器，S3 接口） | Apache-2.0，活跃维护，资源占用小 | 设计初稿推荐；开发者选择不新增存储容器 |
| C RustFS | S3 兼容，Apache-2.0 | 1.0 前后仍有内存占用失控的问题报告（[#8271](https://github.com/rustfs/rustfs/issues/8271)、[#9145](https://github.com/rustfs/rustfs/issues/9145)），不采用 |
| D Garage | 轻量，S3 兼容 | AGPL-3.0 许可证，不采用 |
| E 本地目录 | 文件写在 media 可访问的目录，由 media 签发并提供下载 | **采用，作为默认值** |
| F 阿里云 OSS | 托管对象存储，官方 Go SDK v2 | **采用，作为可选后端，用于正式环境** |

## 决策

1. media 定义存储接口（写入、删除、生成限时下载地址），业务代码只依赖接口；后端由配置 `storage.provider` 选择，取值 `local`（默认）或 `oss`，切换需要重启，不迁移已有文件。
2. `local`：文件写在仓库下的 `data/media/`（被 Git 忽略；容器模式挂载该目录），media 用 HMAC-SHA256 签发下载地址并自己提供下载，支持 `Range`。
3. `oss`：使用官方 SDK `github.com/aliyun/alibabacloud-oss-go-sdk-v2`，Bucket 私有，上传经 media 调用 `PutObject`，下载用 V4 预签名地址，可配置内网写入地址、对象键前缀与自定义下载域名。
4. 不新增存储容器；端口表中预留给 MinIO 的 19000/19001 释放。

## 原因

- **开箱即用**：默认的本地目录不需要云账号，也不新增容器，适合本机内存紧张的开发环境和面试演示。
- **贴近生产**：OSS 是国内常见的生产选择，下载流量不经过 media，容量与可用性由云服务保证；接口抽象让两种后端共用同一套上传规则、权限和签名地址模型。
- **上传统一经 media 中转**：两种后端走同一条上传路径（限大小、按内容识别类型、同步生成缩略图），差别只在写入和签发下载地址两处，见设计 5.2。
- **不用通用 S3 客户端**：两种后端都不需要 S3 协议；OSS 用官方 SDK 可以直接使用其签名 V4 与响应头覆盖参数。

## 代价与风险

- `local` 的下载流量经过 media；多实例必须共享同一个目录，只适合单机部署。
- `oss` 需要 AccessKey 与费用；开发机经公网上传较慢。2019 年 9 月 23 日之后创建的 Bucket，用默认域名直接打开图片会被强制下载，浏览器内预览需要绑定已备案的自定义域名（见设计 3.2）。
- 两种后端的文件互不可见，切换存储后旧文件无法下载；本 ADR 不提供迁移工具。
- 失去了“兼容 S3 即可替换”的通用性：改用其他云存储时要新增一个存储实现。

## 适用条件（何时需要重新评估）

- 需要多台机器部署 media 且不使用云存储：改用共享文件系统，或引入自建对象存储（重新比较 SeaweedFS 等）。
- 需要接入 OSS 之外的云存储：新增存储实现；种类多时再考虑通用的 S3 客户端。
- 文件上限提高到数百 MB 或需要断点续传：评估预签名直传（设计 5.2 方案 B）。
