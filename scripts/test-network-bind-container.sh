#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
binary=${SINGBOX_BIN:?Set SINGBOX_BIN to a pre-verified official Linux sing-box binary}
test_case=${NETWORK_BIND_CASE:-direct}
case "$test_case" in direct|socks|socks-production|forward|forward-tcp) ;; *) echo 'NETWORK_BIND_CASE must be direct, socks, socks-production, forward or forward-tcp' >&2; exit 2;; esac
client_args=()
if [[ "$test_case" == socks-production ]]; then
 : "${SSLOCAL_BIN:?Set SSLOCAL_BIN to a pre-verified official shadowsocks-rust 1.25.0 binary}"
 client_args=(-v "$SSLOCAL_BIN:/fixture-sslocal:ro")
fi
arch=$(docker info --format '{{.Architecture}}')
case "$arch" in aarch64|arm64) arch=arm64;; x86_64|amd64) arch=amd64;; *) exit 1;; esac
work=$(mktemp -d)
container="ctlvps-network-bind-test-$$"
trap 'docker rm -f "$container" >/dev/null 2>&1 || true; rm -rf "$work"' EXIT
GOOS=linux GOARCH="$arch" CGO_ENABLED=0 go build -o "$work/bind-test" ./scripts/network-bind-test
# Only the fixture gets privileged namespace setup, never the host. The actual
# proxy drops to nobody with the production capabilities and seccomp filter.
docker build -t ctlvps-network-bind-test:local - <<'DOCKERFILE'
FROM ubuntu:24.04
RUN apt-get update && apt-get install -y --no-install-recommends iproute2 nftables util-linux ca-certificates && rm -rf /var/lib/apt/lists/*
RUN groupadd -g 199 ctlvps-net && useradd -u 199 -g ctlvps-net -M -d /nonexistent -s /usr/sbin/nologin ctlvps-net
DOCKERFILE
docker run --rm --name "$container" --network none --privileged --read-only --tmpfs /tmp --tmpfs /run \
 -e CTLVPS_BIND_FIXTURE=1 -e CTLVPS_BIND_CASE="$test_case" -v "$work/bind-test:/bind-test:ro" -v "$binary:/fixture-sing-box:ro" "${client_args[@]}" \
 ctlvps-network-bind-test:local /bind-test
