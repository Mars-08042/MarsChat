# 服务运行镜像，由 make images 构建，五个服务共用，用 SERVICE 指定服务名。
# 二进制在宿主机交叉编译后放进构建上下文 build/linux：Docker Desktop 虚拟机内存有限，
# 在容器里编译整个 module 容易把内存用尽，宿主机编译还能复用本机的模块缓存和 Go 代理设置。
# 镜像里只有静态二进制、CA 证书、时区数据和 nonroot 用户，没有 shell。
# 配置目录在运行时以只读方式挂载到 /app/configs（见 compose.yaml），改配置不用重建镜像。
#
# distroless 没有带版本号的标签，按 digest 锁定；升级时重新拉取 nonroot 标签并替换 digest。
FROM gcr.io/distroless/static-debian13:nonroot@sha256:aafe993665bf6200db38a02c11a9351e782a84b6a45e7f33b6695638c88e3931

ARG SERVICE
COPY ${SERVICE} /app/service

USER nonroot:nonroot
ENTRYPOINT ["/app/service", "-conf", "/app/configs"]
