.PHONY: build test web dev fmt
GO ?= go
build: web
	mkdir -p bin
	$(GO) build -trimpath -o bin/drop ./cmd/drop
	$(GO) build -trimpath -o bin/drop-worker ./cmd/drop-worker
web:
	npm --prefix web ci
	npm --prefix web run build
test:
	$(GO) test -race ./...
	npm --prefix web run typecheck
fmt:
	$(GO) fmt ./...
dev:
	python3 scripts/dev.py
