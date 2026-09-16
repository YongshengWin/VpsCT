#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
assets=$(cd "${1:?candidate release directory required}" && pwd)
legacy=$(cd "${2:?official v0.1.1 assets directory required}" && pwd)
container="ctlvps-upgrade-test-$$"
trap 'docker rm -f "$container" >/dev/null 2>&1 || true' EXIT
docker build -f scripts/maintenance-test/Dockerfile -t ctlvps-upgrade-test:local . >/dev/null
docker run -d --name "$container" --privileged --cgroupns=private --tmpfs /run --tmpfs /run/lock --tmpfs /tmp \
 --mount "type=bind,src=$PWD,dst=/src,readonly" --mount "type=bind,src=$assets,dst=/assets,readonly" \
 --mount "type=bind,src=$legacy,dst=/legacy,readonly" ctlvps-upgrade-test:local >/dev/null
for ((i=0;i<30;i++)); do
 if docker exec "$container" test -d /run/systemd/system; then break; fi
 sleep 1
done
docker exec "$container" python3 /src/scripts/upgrade-test/run.py
