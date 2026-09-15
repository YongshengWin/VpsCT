#!/usr/bin/env bash
# Full local verification: go vet/test, TypeScript typecheck, Vite build.
set -euo pipefail
cd "$(dirname "$0")/.."

echo "==> web dependencies and build (required before Go embeds the SPA)"
(cd web && npm ci --no-audit --no-fund && npm test && npm run typecheck && npm run build)

echo "==> go vet"
go vet ./...

echo "==> go test"
go test ./... -count=1

echo "==> go build (with embedded web)"
check_dir=$(mktemp -d)
trap 'rm -rf "$check_dir"' EXIT
CGO_ENABLED=0 go build -o "$check_dir/ctlvpsd" ./cmd/ctlvpsd
for arch in amd64 arm64; do
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -o "$check_dir/ctlvpsd-linux-$arch" ./cmd/ctlvpsd
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -o "$check_dir/ctlvps-agent-linux-$arch" ./cmd/ctlvps-agent
done

echo "==> installer validation"
bash -n install.sh
bash scripts/test-installer.sh

echo "OK"
