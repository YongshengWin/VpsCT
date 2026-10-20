#!/usr/bin/env bash
# Real installer, TLS downloads, enrollment and update in a disposable OpenRC host.
set -euo pipefail
cd "$(dirname "$0")/.."
fixture=$(mktemp -d)
chmod 0755 "$fixture"
container="ctlvps-alpine-installer-$$"
cleanup() { docker rm -f "$container" >/dev/null 2>&1 || true; rm -rf -- "$fixture"; }
trap cleanup EXIT
arch=${ALPINE_TEST_ARCH:-$(docker info --format '{{.Architecture}}')}
case "$arch" in aarch64|arm64) arch=arm64;; x86_64|amd64) arch=amd64;; *) echo 'unsupported Docker architecture' >&2; exit 1;; esac
export CGO_ENABLED=0 GOOS=linux GOARCH="$arch"
for version in v0.0.0-fixture.1 v0.0.0-fixture.2; do
  target="$fixture/$version"
  mkdir -p "$target"
  go build -ldflags "-s -w -X ctlvps/internal/buildinfo.Version=$version" -o "$target/ctlvps-agent-linux-$arch" ./cmd/ctlvps-agent
  go build -ldflags "-s -w -X ctlvps/internal/buildinfo.Version=$version" -o "$target/ctlvps-verify-linux-$arch" ./cmd/ctlvps-verify
  cp uninstall.sh "$target/uninstall.sh"
done
docker build --platform "linux/$arch" -t "ctlvps-alpine-test:$arch" scripts/alpine-test
docker run -d --platform "linux/$arch" --name "$container" --network none \
  --privileged --cgroupns=private --memory 256m --memory-swap 256m \
  --tmpfs /run --tmpfs /tmp:rw,noexec,nosuid,nodev --tmpfs /run/lock \
  --mount "type=bind,src=$fixture,dst=/fixtures,readonly" \
  --mount "type=bind,src=$PWD,dst=/work,readonly" \
  -e CTLVPS_ALPINE_FIXTURE=1 -e "FIXTURE_ARCH=$arch" "ctlvps-alpine-test:$arch" >/dev/null
for ((attempt=0;attempt<30;attempt++)); do
  if docker exec "$container" test -f /run/openrc/softlevel; then break; fi
  sleep 1
done
docker exec "$container" sh -ec 'mount -o remount,rw /sys/fs/cgroup; python3 /work/scripts/alpine-test/prepare.py'
docker exec "$container" timeout 240s python3 /work/scripts/alpine-test/installer.py
docker inspect --format 'container OOMKilled={{.State.OOMKilled}} memory={{.HostConfig.Memory}}' "$container"
