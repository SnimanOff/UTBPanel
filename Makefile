VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
PKG     := github.com/SnimanOff/UTBPanel/backend/internal/buildinfo
LDFLAGS := -s -w -X $(PKG).Version=$(VERSION) -X $(PKG).Commit=$(COMMIT) -X $(PKG).Date=$(DATE)

.PHONY: build test lint gen

build:
	cd backend && CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o ../bin/ ./cmd/edge ./cmd/core ./cmd/panel
	cd backend && CGO_ENABLED=0 GOOS=windows go build -trimpath -ldflags "$(LDFLAGS)" -o ../bin/gate.exe ./cmd/gate

test:
	cd backend && go test ./...

lint:
	cd backend && golangci-lint run ./...

gen:
	cd backend && go generate ./...
