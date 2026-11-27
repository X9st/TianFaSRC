.PHONY: dev-web dev-server web build test vet race fmt openapi openapi-check backup restore-drill

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
	python3 -m unittest discover -s scripts -p 'test_*.py'
	go test ./...
	cd web && npm run typecheck

vet:
	go vet ./...

race:
	go test -race ./...

openapi:
	go run ./cmd/openapi

openapi-check:
	go run ./cmd/openapi -check

backup:
	./scripts/backup.sh

restore-drill:
	./scripts/restore-drill.sh "$(BACKUP_DIR)"

fmt:
	gofmt -w $$(find cmd internal -name '*.go' -type f)

