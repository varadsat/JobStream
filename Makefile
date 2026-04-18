.PHONY: proto test lint up down logs install-hooks

# Path to protoc's bundled well-known types (timestamp.proto etc.).
# Override via: make proto PROTOC_INCLUDE=/your/path
PROTOC_INCLUDE ?= $(HOME)/Downloads/protoc-33.2-win64/include

# ── Codegen ──────────────────────────────────────────────────────────────────
# Prefer buf (brew install bufbuild/buf/buf) — falls back to protoc.
proto:
	@if command -v buf >/dev/null 2>&1; then \
		buf generate; \
	else \
		mkdir -p gen/go && \
		protoc \
			-I proto \
			-I "$(PROTOC_INCLUDE)" \
			--go_out=gen/go --go_opt=paths=source_relative \
			--go-grpc_out=gen/go --go-grpc_opt=paths=source_relative,require_unimplemented_servers=false \
			jobstream/v1/application.proto \
			jobstream/v1/intake.proto; \
	fi

# ── Tests ────────────────────────────────────────────────────────────────────
# "go test all" covers every module listed in go.work.
# Falls back gracefully when no modules are registered yet.
test:
	go test all

# ── Linting ──────────────────────────────────────────────────────────────────
lint:
	golangci-lint run ./...

# ── Local infra ──────────────────────────────────────────────────────────────
up:
	docker compose -f deploy/docker-compose.yml up -d

down:
	docker compose -f deploy/docker-compose.yml down

logs:
	docker compose -f deploy/docker-compose.yml logs -f

# ── One-time dev setup ───────────────────────────────────────────────────────
install-hooks:
	git config core.hooksPath .githooks
	git update-index --chmod=+x .githooks/pre-commit
	@echo "Git hooks installed from .githooks/"
