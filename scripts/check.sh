#!/usr/bin/env bash
# Full local verification: go vet/test, TypeScript typecheck, Vite build.
set -euo pipefail
cd "$(dirname "$0")/.."

echo "==> web dependencies and build (required before Go embeds the SPA)"
(cd web && npm ci --no-audit --no-fund && npm test && npm run typecheck && npm run build)

echo "==> go vet"
go vet ./...

echo "==> agent API contract freshness"
go run ./scripts/generate-agent-contracts.go -check

echo "==> agent skill HTTP client safety"
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s skills/vpsct/scripts -p 'test_*.py'
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s skills -p 'test_install*.py'

echo "==> go test"
test_parallelism=${CTLVPS_TEST_PARALLELISM:-}
# Keep the Go default unless the caller selects bounded package concurrency.
if [[ -n "$test_parallelism" ]]; then
  [[ "$test_parallelism" =~ ^[1-9][0-9]*$ ]] || { echo 'CTLVPS_TEST_PARALLELISM must be a positive integer' >&2; exit 1; }
  go test -p "$test_parallelism" ./... -count=1 -timeout=5m
else
  go test ./... -count=1 -timeout=5m
fi

echo "==> go build (with embedded web)"
check_dir=$(mktemp -d)
trap 'rm -rf "$check_dir"' EXIT
CGO_ENABLED=0 go build -o "$check_dir/ctlvpsd" ./cmd/ctlvpsd
for arch in amd64 arm64; do
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -o "$check_dir/ctlvpsd-linux-$arch" ./cmd/ctlvpsd
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -o "$check_dir/ctlvps-agent-linux-$arch" ./cmd/ctlvps-agent
done

echo "==> installer validation"
bash -n install.sh uninstall.sh
bash scripts/test-installer.sh
bash scripts/test-uninstaller.sh

echo "OK"
