#!/usr/bin/env bash
set -euo pipefail
# Isolated, temporary Linux namespace. No host networking or real VPS access.
cd "$(dirname "$0")/.."
arch=$(docker info --format '{{.Architecture}}')
case "$arch" in
  aarch64|arm64) arch=arm64 ;;
  x86_64|amd64) arch=amd64 ;;
  *) echo "unsupported Docker architecture" >&2; exit 1 ;;
esac
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go test -c ./internal/netinventory -o "$work/network.test"
docker build -t ctlvps-network-test:local - <<'DOCKERFILE'
FROM alpine:3.23
RUN apk add --no-cache iproute2
DOCKERFILE
docker run --rm --network none --cap-drop ALL --cap-add NET_ADMIN --read-only --tmpfs /tmp \
  -e CTLVPS_NETWORK_NAMESPACE_TEST=1 -v "$work/network.test:/network.test:ro" \
  ctlvps-network-test:local /network.test -test.v
