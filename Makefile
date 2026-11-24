.PHONY: dev-web dev-server web build test fmt openapi openapi-check

dev-web:
	cd web && npm run dev

dev-server:
	go run ./cmd/server

web:
	cd web && npm run build
	./scripts/sync-web.sh

build: web
	mkdir -p bin
	go build -trimpath -o bin/tianfa-server ./cmd/server

test:
	go run ./cmd/openapi -check
	go test ./...
	cd web && npm run typecheck

openapi:
	go run ./cmd/openapi

openapi-check:
	go run ./cmd/openapi -check

fmt:
	gofmt -w $$(find cmd internal -name '*.go' -type f)

