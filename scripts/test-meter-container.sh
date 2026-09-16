#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
docker build -q -t ctlvps-meter-test:local scripts/meter-test >/dev/null
arch=$(docker info --format '{{.Architecture}}')
case "$arch" in aarch64|arm64) arch=arm64;; x86_64|amd64) arch=amd64;; *) echo "Unsupported Docker architecture" >&2; exit 1;; esac
GOOS=linux GOARCH="$arch" CGO_ENABLED=0 go test -c -o "$work/nft.test" ./internal/nft
extra=()
if [[ -n "${SINGBOX_BIN:-}" ]]; then
  extra=(-v "$SINGBOX_BIN:/test-sing-box:ro" -e CTLVPS_SINGBOX=/test-sing-box)
fi
for test_name in TestKernelNodeAccounting TestKernelEgressBoundary; do
 docker run "${extra[@]}" --rm --privileged --cgroupns=private --network none -e CTLVPS_KERNEL_TEST=1 -v "$PWD:/work:ro" -v "$work:/test:ro" ctlvps-meter-test:local /test/nft.test -test.run "^${test_name}$" -test.v
done
