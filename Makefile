# MarsChat 开发命令。Windows 上用 Git Bash 的 sh 执行，和 Linux CI 行为一致。
SHELL := sh

SERVICES := access account social message delivery media
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# 代码生成工具固定版本；protoc 插件版本写在 buf.gen.*.yaml 中
WIRE := go run github.com/google/wire/cmd/wire@v0.7.0

.DEFAULT_GOAL := help

.PHONY: help
help: ## 列出可用命令
	@grep -E '^[a-zA-Z0-9_%-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "} {printf "  %-14s %s\n", $$1, $$2}'

.PHONY: conf
conf: ## 生成配置 proto 的 Go 代码
	buf generate --template buf.gen.conf.yaml

.PHONY: api
api: ## 生成接口 proto 的 gRPC、HTTP 和 OpenAPI 代码
	buf generate --template buf.gen.yaml

.PHONY: wire
wire: ## 生成依赖注入代码
	$(WIRE) $(foreach s,$(SERVICES),./app/$(s)/cmd/$(s))

.PHONY: gen
gen: conf api wire ## 重新生成全部代码

.PHONY: lint
lint: ## proto 规范检查
	buf lint

.PHONY: vet
vet: ## go vet
	go vet ./...

.PHONY: test
test: ## 单元测试
	go test ./...

.PHONY: e2e
e2e: ## 端到端测试，需要先 make up、迁移 account、social、message、media 并启动全部服务
	@test -f .env || { echo "missing .env: copy .env.example to .env and set the passwords"; exit 1; }
	set -a && . ./.env && set +a && go test -tags e2e -count=1 ./test/e2e/...

.PHONY: build
build: ## 构建全部服务到 bin/
	@mkdir -p bin
	go build -ldflags "-X main.Version=$(VERSION)" -o bin/ $(foreach s,$(SERVICES),./app/$(s)/cmd/$(s))

# 容器模式（profile apps）：镜像用宿主机交叉编译的二进制，见 deploy/docker/Dockerfile。
# 目标架构默认 amd64，与 Docker Desktop 虚拟机一致；ARM 机器上用 make images IMAGE_ARCH=arm64。
IMAGE_TAG ?= dev
IMAGE_ARCH ?= amd64
APP_SERVICES := $(SERVICES) apisix

.PHONY: images
images: ## 交叉编译全部服务并构建镜像 marschat/<service>:dev
	@mkdir -p build/linux
	CGO_ENABLED=0 GOOS=linux GOARCH=$(IMAGE_ARCH) go build -trimpath -ldflags "-s -w -X main.Version=$(VERSION)" -o build/linux/ $(foreach s,$(SERVICES),./app/$(s)/cmd/$(s))
	for s in $(SERVICES); do docker build -q -f deploy/docker/Dockerfile --build-arg SERVICE=$$s -t marschat/$$s:$(IMAGE_TAG) build/linux || exit 1; done

.PHONY: certs
certs: ## 生成网关的自签证书到 deploy/apisix/certs（已有时不覆盖）
	go run ./tools/devcert -out deploy/apisix/certs

.PHONY: cluster-up
cluster-up: certs ## 容器模式启动全部服务与网关（先 make up、迁移、make images；不要同时在宿主机运行服务）
	@test -f .env || { echo "missing .env: copy .env.example to .env and set the passwords"; exit 1; }
	docker compose --profile apps up -d --wait

.PHONY: cluster-test
cluster-test: ## 集群测试与故障实验，需要先 make cluster-up；只经网关访问，-v 输出实验数据
	@test -f .env || { echo "missing .env: copy .env.example to .env and set the passwords"; exit 1; }
	set -a && . ./.env && set +a && go test -tags cluster -count=1 -v -timeout 20m ./test/cluster/...

.PHONY: cluster-down
cluster-down: ## 停止并删除容器模式的服务与网关容器，依赖组件保持运行
	docker compose --profile apps rm -sf $(APP_SERVICES)

.PHONY: up
up: ## 启动依赖组件，等待健康检查通过后创建 Kafka topic
	docker compose up -d --wait
	docker compose run --rm kafka-init

.PHONY: down
down: ## 停止依赖组件，保留数据卷
	docker compose down

.PHONY: ps
ps: ## 查看依赖组件状态
	docker compose ps

# 数据库迁移，例如 make migrate-account；回滚一步：make migrate-account ARGS="down 1"
migrate-%: ## 执行服务的数据库迁移，默认 up
	docker compose run --rm migrate-$* $(or $(ARGS),up)

.PHONY: keys
keys: ## 生成开发用 JWT 密钥对与 media 下载地址签名密钥并写入 .env（已有时不覆盖）
	go run ./tools/jwtkey -env .env

.PHONY: grant-admin
grant-admin: ## 把已注册用户设为管理员，例如 make grant-admin EMAIL=someone@example.com
	sh scripts/grant-admin.sh "$(EMAIL)"

# 在宿主机运行服务，自动加载 .env
# 命令行里只用 ASCII：GnuWin32 make 3.81 按 ANSI 代码页（GBK）把命令传给 sh，
# 中文的 UTF-8 字节会和相邻的引号组成非法字符，导致引号丢失、命令解析失败。
run-%: ## 运行服务，例如 make run-account
	@test -f .env || { echo "missing .env: copy .env.example to .env and set the passwords"; exit 1; }
	set -a && . ./.env && set +a && go run -ldflags "-X main.Version=$(VERSION)" ./app/$*/cmd/$* -conf app/$*/configs
