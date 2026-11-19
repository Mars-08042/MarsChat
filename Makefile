# MarsChat 开发命令。Windows 上用 Git Bash 的 sh 执行，和 Linux CI 行为一致。
SHELL := sh

SERVICES := account social
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
e2e: ## 端到端测试，需要先 make up、迁移各服务（make migrate-account、make migrate-social）并启动服务
	@test -f .env || { echo "missing .env: copy .env.example to .env and set the passwords"; exit 1; }
	set -a && . ./.env && set +a && go test -tags e2e -count=1 ./test/e2e/...

.PHONY: build
build: ## 构建全部服务到 bin/
	@mkdir -p bin
	go build -ldflags "-X main.Version=$(VERSION)" -o bin/ $(foreach s,$(SERVICES),./app/$(s)/cmd/$(s))

.PHONY: up
up: ## 启动依赖组件并等待健康检查通过
	docker compose up -d --wait

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
keys: ## 生成开发用 JWT 密钥对并写入 .env（已有时不覆盖）
	go run ./tools/jwtkey -env .env

.PHONY: grant-admin
grant-admin: ## 把已注册用户设为管理员，例如 make grant-admin PHONE=13800000000
	sh scripts/grant-admin.sh "$(PHONE)"

# 在宿主机运行服务，自动加载 .env
# 命令行里只用 ASCII：GnuWin32 make 3.81 按 ANSI 代码页（GBK）把命令传给 sh，
# 中文的 UTF-8 字节会和相邻的引号组成非法字符，导致引号丢失、命令解析失败。
run-%: ## 运行服务，例如 make run-account
	@test -f .env || { echo "missing .env: copy .env.example to .env and set the passwords"; exit 1; }
	set -a && . ./.env && set +a && go run -ldflags "-X main.Version=$(VERSION)" ./app/$*/cmd/$* -conf app/$*/configs
