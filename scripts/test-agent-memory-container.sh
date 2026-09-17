#!/usr/bin/env bash
# Offline, disposable 192 MiB container. Never touches a real VPS.
set -euo pipefail
cd "$(dirname "$0")/.."
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
arch=$(docker info --format '{{.Architecture}}')
case "$arch" in aarch64|arm64) arch=arm64;; x86_64|amd64) arch=amd64;; *) exit 1;; esac
CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go test -c -o "$work/agentnet.test" ./internal/agentnet
docker build -q -f scripts/maintenance-test/Dockerfile -t ctlvps-maintenance-test:local . >/dev/null
docker run --rm --network none --memory=192m --memory-swap=192m \
 -e CTLVPS_MEMORY_TEST=1 -e GOMEMLIMIT=64MiB \
 --mount "type=bind,src=$work/agentnet.test,dst=/agentnet.test,readonly" \
 ctlvps-maintenance-test:local /agentnet.test -test.run '^TestStreamingMemoryContainer$' -test.v
