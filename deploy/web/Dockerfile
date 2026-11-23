# 前端镜像，由 make web-image 构建（docs/design/web-deployment.md）。
# 与服务镜像的做法一致：产物在宿主机 pnpm build，镜像里只复制 web/dist，不装 Node。
# 构建上下文是 web/dist；nginx 配置从命名上下文 conf（deploy/web）取，需要 BuildKit（Docker Desktop 默认开启）。
# web/public/emoji-packs 存在时会随产物进入镜像（开发者确认已获授权，2026-11-22）。
#
# nginx-unprivileged 以非 root 用户运行、监听 8080，pid 与临时文件都在 /tmp；
# 按版本标签加 digest 锁定，升级时同时替换两者。
FROM nginxinc/nginx-unprivileged:1.30.6-alpine3.24-slim@sha256:5d8bb9437d74a600ec5dbf90c035451c724dc33eac8d3a71078f6f8612c345f6

COPY --from=conf nginx.conf /etc/nginx/conf.d/default.conf
COPY . /usr/share/nginx/html/
