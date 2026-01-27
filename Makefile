# Makefile for roborev development builds

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -X github.com/roborev-dev/roborev/internal/version.Version=$(VERSION)

.PHONY: build install clean test test-integration test-all postgres-up postgres-down

build:
	@mkdir -p bin
	go build -ldflags="$(LDFLAGS)" -o bin/roborev ./cmd/roborev

install:
	@# Install to ~/.local/bin for development (creates directory if needed)
	@mkdir -p "$(HOME)/.local/bin"
	go build -ldflags="$(LDFLAGS)" -o "$(HOME)/.local/bin/roborev" ./cmd/roborev
	@echo "Installed to ~/.local/bin/roborev"

clean:
	rm -rf bin/

# Unit tests only (excludes integration tests)
test:
	go test ./...

# Start postgres for integration tests
postgres-up:
	docker compose -f docker-compose.test.yml up -d --wait

# Stop postgres
postgres-down:
	docker compose -f docker-compose.test.yml down

# Integration tests (requires postgres running)
test-integration: postgres-up
	@echo "Waiting for postgres to be ready..."
	@sleep 2
	TEST_POSTGRES_URL="postgres://roborev_test:roborev_test_password@localhost:5433/roborev_test" \
		go test -tags=integration -v ./internal/storage/... -run Integration

# Run all tests including integration
test-all: test test-integration

# CI target: run integration tests without managing docker (assumes postgres is running)
test-integration-ci:
	go test -tags=integration -v ./internal/storage/... -run Integration
