SHELL := /bin/bash
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "")
LDFLAGS := -s -w -X ctlvps/internal/buildinfo.Version=$(VERSION) -X ctlvps/internal/buildinfo.Commit=$(COMMIT)
GOFLAGS := -trimpath
BIN     := bin
SECURITY_EPOCH ?= 1
REPOSITORY ?= $(GITHUB_REPOSITORY)

.PHONY: all web server agent agents build dev dev-web test vet check lint clean docker release

all: build

## web: build the SPA into web/dist (embedded into ctlvpsd)
web:
	cd web && npm ci --no-audit --no-fund && npm run build

## server: build ctlvpsd for the host platform (requires web/dist)
server:
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BIN)/ctlvpsd ./cmd/ctlvpsd

## agent: build ctlvps-agent for the host platform
agent:
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BIN)/ctlvps-agent ./cmd/ctlvps-agent

## agents: cross-compile static linux agents served at /dl/agent/{platform}
agents:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BIN)/ctlvps-agent-linux-amd64 ./cmd/ctlvps-agent
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BIN)/ctlvps-agent-linux-arm64 ./cmd/ctlvps-agent

## build: web + server + linux agents
build: web
	$(MAKE) server agents

## release: linux/amd64 + linux/arm64 server binaries plus agents
release: web
	$(MAKE) agents
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BIN)/ctlvpsd-linux-amd64 ./cmd/ctlvpsd
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BIN)/ctlvpsd-linux-arm64 ./cmd/ctlvpsd
	for arch in amd64 arm64; do CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build $(GOFLAGS) -o $(BIN)/ctlvps-verify-linux-$$arch ./cmd/ctlvps-verify; CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build $(GOFLAGS) -o $(BIN)/ctlvps-sign-linux-$$arch ./cmd/ctlvps-sign; done
	python3 scripts/package-release.py --version "$(VERSION)" --repository "$(REPOSITORY)" --security-epoch "$(SECURITY_EPOCH)"

## dev: run the API with hot-reloading SPA proxied from Vite (run `make dev-web` in another shell)
dev:
	go run ./cmd/ctlvpsd --data ./data --dev-proxy http://127.0.0.1:5173 --log-level debug --agent-bin-dir $(BIN)

dev-web:
	cd web && npm run dev

test:
	go test ./... -count=1

vet:
	go vet ./...

## check: fast checks; install web dependencies first with npm ci
check: vet test
	cd web && npm run typecheck

docker:
	docker build -t ctlvps:$(VERSION) .

clean:
	rm -rf $(BIN) web/dist/assets web/dist/index.html
