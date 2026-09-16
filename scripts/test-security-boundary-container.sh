#!/usr/bin/env bash
# Tests only the two suspected boundaries, on an offline disposable machine.
set -euo pipefail
cd "$(dirname "$0")/.."
fixture=$(mktemp -d)
container="ctlvps-boundary-test-$$"
cleanup() { docker rm -f "$container" >/dev/null 2>&1 || true; rm -rf -- "$fixture"; }
trap cleanup EXIT
mode=${CTLVPS_BOUNDARY_MODE:-baseline}
if [[ -n "${PEBBLE_BIN:-}" && ( "$mode" != candidate || -z "${SINGBOX_BIN:-}" ) ]]; then
 echo 'PEBBLE_BIN requires candidate mode and SINGBOX_BIN' >&2
 exit 1
fi
go run ./scripts/security-boundary-test "$fixture" "$mode"
arch=$(docker info --format '{{.Architecture}}')
case "$arch" in aarch64|arm64) arch=arm64;; x86_64|amd64) arch=amd64;; *) exit 1;; esac
CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -o "$fixture/ctlvps-agent" ./scripts/proxy-hardening-test
if [[ -n "${SINGBOX_BIN:-}" ]]; then cp -- "$SINGBOX_BIN" "$fixture/sing-box"; fi
if [[ -n "${SNELL_BIN:-}" ]]; then cp -- "$SNELL_BIN" "$fixture/snell-server"; fi
if [[ -n "${SNELL_CLIENT_BIN:-}" ]]; then cp -- "$SNELL_CLIENT_BIN" "$fixture/snell-client"; fi
if [[ -n "${PEBBLE_BIN:-}" ]]; then cp -- "$PEBBLE_BIN" "$fixture/pebble"; fi
docker build -f scripts/maintenance-test/Dockerfile -t ctlvps-boundary-test:local .
docker run -d --name "$container" --network none --privileged --cgroupns=private \
 --tmpfs /run --tmpfs /run/lock --tmpfs /tmp \
 --mount "type=bind,src=$PWD,dst=/src,readonly" \
 --mount "type=bind,src=$fixture,dst=/fixtures,readonly" ctlvps-boundary-test:local >/dev/null
for ((i=0;i<30;i++)); do
 if docker exec "$container" test -d /run/systemd/system; then break; fi
 sleep 1
done
docker exec -e "CTLVPS_CANDIDATE=$([[ "$mode" == candidate ]] && echo 1 || echo 0)" "$container" python3 /src/scripts/security-boundary-test/run.py
if [[ -n "${PEBBLE_BIN:-}" ]]; then
 docker exec "$container" python3 /src/scripts/security-boundary-test/acme_renewal.py
fi
