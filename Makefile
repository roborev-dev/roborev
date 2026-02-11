# Makefile for roborev development builds

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -X github.com/roborev-dev/roborev/internal/version.Version=$(VERSION)

.PHONY: build install clean test test-integration test-postgres test-all postgres-up postgres-down test-postgres-ci

build:
	@mkdir -p bin
	go build -ldflags="$(LDFLAGS)" -o bin/roborev ./cmd/roborev

install:
	@# Install to ~/.local/bin for development (creates directory if needed)
	@if [ -z "$(HOME)" ]; then echo "error: HOME is not set" >&2; exit 1; fi
	@mkdir -p "$(HOME)/.local/bin"
	go build -ldflags="$(LDFLAGS)" -o "$(HOME)/.local/bin/roborev" ./cmd/roborev
	@echo "Installed to ~/.local/bin/roborev"

clean:
	rm -rf bin/

# Unit tests only (excludes integration and postgres tests)
test:
	go test ./...

# Unit + slow integration tests (no postgres required)
test-integration:
	go test -tags=integration ./...

# Start postgres for postgres tests
postgres-up:
	docker compose -f docker-compose.test.yml up -d --wait

# Stop postgres
postgres-down:
	docker compose -f docker-compose.test.yml down

# Postgres tests (requires postgres running)
test-postgres: postgres-up
	@echo "Waiting for postgres to be ready..."
	@sleep 2
	TEST_POSTGRES_URL="postgres://roborev_test:roborev_test_password@localhost:5433/roborev_test" \
		go test -tags=postgres -v ./internal/storage/... -run Integration

# Run all tests (unit + integration + postgres)
test-all: test-integration test-postgres

# CI target: run postgres tests without managing docker (assumes postgres is running)
test-postgres-ci:
	go test -tags=postgres -v ./internal/storage/... -run Integration
