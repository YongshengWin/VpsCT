#!/usr/bin/env bash
# Exercise Snell's kernel accounting boundary without downloading a proxy core.
set -euo pipefail
cd "$(dirname "$0")/.."
work=$(mktemp -d)
container="ctlvps-retirement-test-$$"
trap 'docker rm -f "$container" >/dev/null 2>&1 || true; rm -rf "$work"' EXIT
docker build -q -f scripts/uninstaller-test/Dockerfile -t ctlvps-uninstaller-test:local . >/dev/null
arch=$(docker info --format '{{.Architecture}}')
case "$arch" in aarch64|arm64) arch=arm64;; x86_64|amd64) arch=amd64;; *) exit 1;; esac
GOOS=linux GOARCH="$arch" CGO_ENABLED=0 go test -c -o "$work/core.test" ./internal/core
docker run -d --name "$container" --privileged --cgroupns=private --network none --tmpfs /run --tmpfs /run/lock --tmpfs /tmp ctlvps-uninstaller-test:local >/dev/null
docker cp "$work/core.test" "$container:/core.test"
for ((attempt=0; attempt<30; attempt++)); do
  if docker exec "$container" test -d /run/systemd/system; then break; fi
  sleep 1
done
docker exec -e CTLVPS_SYSTEMD_TEST=1 "$container" /core.test -test.run '^TestSnellMeterSurvivesRestart$' -test.v
