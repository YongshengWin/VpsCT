#!/usr/bin/env bash
# Real systemd, cgroup, nftables and seccomp; never run on a real VPS.
set -euo pipefail
cd "$(dirname "$0")/.."
fixture=$(mktemp -d)
chmod 0755 "$fixture"
container="ctlvps-issue-regressions-$$"
cleanup() { docker rm -f "$container" >/dev/null 2>&1 || true; rm -rf -- "$fixture"; }
trap cleanup EXIT
arch=${ISSUE_TEST_ARCH:-$(docker info --format '{{.Architecture}}')}
case "$arch" in aarch64|arm64) arch=arm64;; x86_64|amd64) arch=amd64;; *) exit 1;; esac
export CGO_ENABLED=0 GOOS=linux GOARCH="$arch"
go test -c -o "$fixture/core.test" ./internal/core
go test -c -o "$fixture/nft.test" ./internal/nft
go test -c -o "$fixture/sandbox.test" ./internal/proxysandbox
image="ctlvps-issue-regressions:$arch"
docker build --platform "linux/$arch" -f scripts/maintenance-test/Dockerfile -t "$image" .
docker run -d --platform "linux/$arch" --name "$container" --network none --privileged --cgroupns=private \
  --tmpfs /run --tmpfs /run/lock --tmpfs /tmp \
  --mount "type=bind,src=$fixture,dst=/fixtures,readonly" "$image" >/dev/null
docker exec "$container" timeout 45s sh -c 'systemctl is-system-running --wait; result=$?; [ "$result" -le 1 ]'
docker exec -e CTLVPS_SYSTEMD_TEST=1 "$container" /fixtures/core.test -test.run '^TestKernelProxy(SliceValidation|PreflightDiagnostics)$' -test.v
docker exec -e CTLVPS_INGRESS_TEST=1 "$container" unshare --net sh -ec 'ip link set lo up; exec /fixtures/nft.test -test.run "^TestKernel(ForeignIngress|IngressLifecycle)$" -test.v'
docker exec "$container" /fixtures/sandbox.test -test.run '^TestKernelSocketSandbox$' -test.v
