# Makefile for roborev development builds

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -X github.com/wesm/roborev/internal/version.Version=$(VERSION)

.PHONY: build install clean test

build:
	@mkdir -p bin
	go build -ldflags="$(LDFLAGS)" -o bin/roborev ./cmd/roborev
	go build -ldflags="$(LDFLAGS)" -o bin/roborevd ./cmd/roborevd

install:
	go install -ldflags="$(LDFLAGS)" ./cmd/roborev
	go install -ldflags="$(LDFLAGS)" ./cmd/roborevd

clean:
	rm -rf bin/

test:
	go test ./...
