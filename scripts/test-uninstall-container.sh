#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
image=ctlvps-uninstaller-test:local
container="ctlvps-uninstaller-test-$$"
trap 'docker rm -f "$container" >/dev/null 2>&1 || true' EXIT
docker build -f scripts/uninstaller-test/Dockerfile -t "$image" .
# Private network/mount/cgroup namespaces; no host paths are writable in the test.
docker run -d --name "$container" --privileged --cgroupns=private \
  --tmpfs /run --tmpfs /run/lock --tmpfs /tmp \
  --mount "type=bind,src=$PWD,dst=/src,readonly" "$image" >/dev/null
for ((attempt=0; attempt<30; attempt++)); do
  if docker exec "$container" test -d /run/systemd/system; then break; fi
  sleep 1
done
docker exec "$container" bash /src/scripts/uninstaller-test/run.sh
