#!/usr/bin/env bash
# Real systemd lifecycle tests with a pre-verified, matching Linux sing-box.
set -euo pipefail
cd "$(dirname "$0")/.."
binary=${SINGBOX_BIN:?Set SINGBOX_BIN to an absolute path to a verified official Linux binary}
work=$(mktemp -d)
container="ctlvps-core-test-$$"
trap 'docker rm -f "$container" >/dev/null 2>&1 || true; rm -rf "$work"' EXIT
docker build -q -f scripts/uninstaller-test/Dockerfile -t ctlvps-uninstaller-test:local . >/dev/null
arch=$(docker info --format '{{.Architecture}}')
case "$arch" in aarch64|arm64) arch=arm64;; x86_64|amd64) arch=amd64;; *) exit 1;; esac
GOOS=linux GOARCH="$arch" CGO_ENABLED=0 go test -c -o "$work/core.test" ./internal/core
GOOS=linux GOARCH="$arch" CGO_ENABLED=0 go build -o "$work/ctlvps-agent" ./cmd/ctlvps-agent
docker run -d --name "$container" --privileged --cgroupns=private --network none --tmpfs /run --tmpfs /run/lock --tmpfs /tmp ctlvps-uninstaller-test:local >/dev/null
docker cp "$binary" "$container:/fixture-sing-box"
docker cp "$work/ctlvps-agent" "$container:/usr/local/bin/ctlvps-agent"
docker exec "$container" chown root:root /usr/local/bin/ctlvps-agent
docker exec "$container" chmod 0755 /usr/local/bin/ctlvps-agent
docker cp "$work/core.test" "$container:/core.test"
docker exec "$container" ln -s /fixture-sing-box /usr/local/bin/sing-box
docker exec -e CTLVPS_SYSTEMD_TEST=1 "$container" /core.test -test.run 'TestSharedServiceLifecycle|TestSingBoxConfigPassesCheck|TestSnellMeterSurvivesRestart|TestNativeACMEStateMount' -test.v
