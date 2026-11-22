.PHONY: all build test clean release

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "v1.0.0")
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X 'main.Version=$(VERSION)' -X 'main.GitCommit=$(COMMIT)' -X 'main.BuildDate=$(BUILD_DATE)'

all: build

build:
	mkdir -p bin
	go build -ldflags="$(LDFLAGS)" -o bin/procloud ./cmd/api

test:
	go test -v ./...

release:
	chmod +x scripts/build-release.sh
	./scripts/build-release.sh $(VERSION)

clean:
	rm -rf bin/ dist/ *.test
