#!/usr/bin/env bash
# Optional upstream compatibility reproducer; a failing run is evidence, not
# a reason to retry application traffic or relax SS2022 header validation.
set -euo pipefail
cd "$(dirname "$0")/.."
: "${SINGBOX_BIN:?Set SINGBOX_BIN to verified official Linux sing-box 1.14.1}"
: "${SSLOCAL_BIN:?Set SSLOCAL_BIN to verified official Linux shadowsocks-rust 1.25.0 sslocal}"
fixture=$(mktemp -d)
container="ctlvps-ss2022-interop-test-$$"
cleanup() { docker rm -f "$container" >/dev/null 2>&1 || true; rm -rf -- "$fixture"; }
trap cleanup EXIT
arch=$(docker info --format '{{.Architecture}}')
case "$arch" in aarch64|arm64) arch=arm64;; x86_64|amd64) arch=amd64;; *) exit 1;; esac
CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -o "$fixture/probe" ./scripts/ss2022-interop-test
cp -- "$SINGBOX_BIN" "$fixture/sing-box"
cp -- "$SSLOCAL_BIN" "$fixture/sslocal"
docker build -f scripts/maintenance-test/Dockerfile -t ctlvps-ss2022-interop-test:local .
docker run --rm --name "$container" --network none --cap-drop ALL \
 --security-opt no-new-privileges --pids-limit 128 --memory 256m \
 -e CTLVPS_SS2022_INTEROP_FIXTURE=1 -e "SS2022_TEST_CLIENT=${SS2022_TEST_CLIENT:-sing-box}" --entrypoint /usr/bin/timeout \
 --mount "type=bind,src=$fixture,dst=/fixture,readonly" \
 ctlvps-ss2022-interop-test:local 190s /fixture/probe
