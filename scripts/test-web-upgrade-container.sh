#!/usr/bin/env bash
# Real historical release -> candidate via web maintenance, entirely offline.
# Caller supplies an already verified official sing-box 1.14.1 for this arch.
set -euo pipefail
cd "$(dirname "$0")/.."
assets=$(cd "${1:?candidate release directory required}" && pwd)
legacy=$(cd "${2:?historical release directory required}" && pwd)
binary=${SINGBOX_BIN:?official sing-box 1.14.1 binary required}
fixture=$(mktemp -d)
container="ctlvps-web-upgrade-test-$$"
cleanup() { docker rm -f "$container" >/dev/null 2>&1 || true; rm -rf -- "$fixture"; }
trap cleanup EXIT
cp -- "$binary" "$fixture/sing-box"
chmod 0755 "$fixture" "$fixture/sing-box"
docker build -f scripts/maintenance-test/Dockerfile -t ctlvps-web-upgrade-test:local . >/dev/null
docker run -d --name "$container" --network none --privileged --cgroupns=private \
 --tmpfs /run --tmpfs /run/lock --tmpfs /tmp \
 --mount "type=bind,src=$PWD,dst=/src,readonly" \
 --mount "type=bind,src=$assets,dst=/assets,readonly" \
 --mount "type=bind,src=$legacy,dst=/legacy,readonly" \
 --mount "type=bind,src=$fixture,dst=/fixtures,readonly" ctlvps-web-upgrade-test:local >/dev/null
bash scripts/wait-systemd-container.sh "$container" >/dev/null
if ! docker exec "$container" python3 /src/scripts/web-upgrade-test/run.py; then
 docker exec "$container" journalctl --no-pager -n 100 -u ctlvpsd -u ctlvps-agent -u ctlvps-singbox -u 'ctlvps-maintenance-job*'
 exit 1
fi
