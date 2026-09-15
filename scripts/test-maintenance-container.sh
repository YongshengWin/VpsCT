#!/usr/bin/env bash
# Real service lifecycle tests, exclusively in a disposable systemd container.
set -euo pipefail
cd "$(dirname "$0")/.."
assets_dir=$(cd "${1:?usage: test-maintenance-container.sh RELEASE_DIRECTORY}" && pwd)
image=ctlvps-maintenance-test:local
container="ctlvps-maintenance-test-$$"
trap 'docker rm -f "$container" >/dev/null 2>&1 || true' EXIT
docker build -f scripts/maintenance-test/Dockerfile -t "$image" .
docker run -d --name "$container" --privileged --cgroupns=private \
  --tmpfs /run --tmpfs /run/lock --tmpfs /tmp \
  --mount "type=bind,src=$PWD,dst=/src,readonly" \
  --mount "type=bind,src=$assets_dir,dst=/assets,readonly" "$image" >/dev/null
for ((attempt=0; attempt<30; attempt++)); do
  if docker exec "$container" test -d /run/systemd/system; then break; fi
  sleep 1
done
docker exec "$container" python3 /src/scripts/maintenance-test/run.py
