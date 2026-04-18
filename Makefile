.PHONY: proto test lint up down logs install-hooks

# ── Codegen ──────────────────────────────────────────────────────────────────
proto:
	buf generate

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
