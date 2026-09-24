#!/usr/bin/env bash
# No real VPS, host network or host systemd is used. The caller supplies an
# already verified official sing-box 1.14.1 binary for the Docker architecture.
set -euo pipefail
cd "$(dirname "$0")/.."
binary=${SINGBOX_BIN:?Set SINGBOX_BIN to a pre-verified official Linux sing-box binary}
client=${NETWORK_TEST_CLIENT:-sing-box}
network_case=${NETWORK_SYSTEMD_CASE:-direct}
case "$network_case" in native-subscriptions|subscriptions|direct|socks|socks-private|forward|forward-udp|forward-socks|forward-ssh|forward-wireguard|ssh|wireguard|mita|wgaccess|transit|transit-ss2022|protocols|listen|reality|forward-dns|forward-private) ;; *) echo 'unsupported NETWORK_SYSTEMD_CASE' >&2; exit 1;; esac
case "$client" in
 sing-box) ;;
 shadowsocks-rust) : "${SSLOCAL_BIN:?Set SSLOCAL_BIN to pre-verified official shadowsocks-rust 1.25.0 sslocal}" ;;
 *) echo 'NETWORK_TEST_CLIENT must be sing-box or shadowsocks-rust' >&2; exit 1 ;;
esac
case "${MIERU_TEST_CLIENT:-native}" in
 native|mihomo) ;;
 *) echo 'unsupported MIERU_TEST_CLIENT' >&2; exit 1 ;;
esac
if [[ "$network_case" == native-subscriptions || "${MIERU_TEST_CLIENT:-}" == mihomo ]]; then
 : "${MIHOMO_BIN:?Set MIHOMO_BIN to a pre-verified official Linux Mihomo binary}"
fi
fixture=$(mktemp -d)
chmod 0755 "$fixture"
container="ctlvps-network-systemd-test-$$"
landing_container="$container-landing"
transit_network="$container-bridge"
mkdir "$fixture/coord"
cleanup() { docker rm -f "$landing_container" >/dev/null 2>&1 || true; docker rm -f "$container" >/dev/null 2>&1 || true; docker network rm "$transit_network" >/dev/null 2>&1 || true; rm -rf -- "$fixture"; }
trap cleanup EXIT
arch=${NETWORK_TEST_ARCH:-$(docker info --format '{{.Architecture}}')}
case "$arch" in aarch64|arm64) arch=arm64;; x86_64|amd64) arch=amd64;; *) exit 1;; esac
CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -o "$fixture/ctlvps-agent" ./cmd/ctlvps-agent
CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -o "$fixture/probe" ./scripts/network-bind-test
CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -o "$fixture/integration" ./scripts/network-systemd-test
CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go test -c -o "$fixture/transport.test" ./internal/agentnet
cp -- "$binary" "$fixture/sing-box"
if [[ "$network_case" == mita || "$network_case" == listen || "$network_case" == native-subscriptions ]]; then cp -- "${MITA_BIN:?Set MITA_BIN}" "$fixture/mita"; cp -- "${MIERU_BIN:?Set MIERU_BIN}" "$fixture/mieru"; fi
if [[ "$network_case" == listen || "$network_case" == native-subscriptions ]]; then cp -- "${SNELL_BIN:?Set SNELL_BIN}" "$fixture/snell-server"; fi
if [[ -n "${MIHOMO_BIN:-}" ]]; then cp -- "$MIHOMO_BIN" "$fixture/mihomo"; fi
if [[ "$client" == shadowsocks-rust ]]; then cp -- "$SSLOCAL_BIN" "$fixture/sslocal"; fi
image="ctlvps-network-systemd-test:$arch"
docker build --platform "linux/$arch" -f scripts/maintenance-test/Dockerfile -t "$image" .
network_args=(--network none)
if [[ "$network_case" == transit || "$network_case" == transit-ss2022 ]]; then
 docker network create --internal --subnet 198.18.37.0/24 "$transit_network" >/dev/null
 network_args=(--network "$transit_network" --ip 198.18.37.2)
 docker run -d --platform "linux/$arch" --name "$landing_container" --network "$transit_network" --ip 198.18.37.3 --privileged --cgroupns=private --tmpfs /run --tmpfs /run/lock --tmpfs /tmp -e CTLVPS_SYSTEMD_NETWORK_FIXTURE=1 -e CTLVPS_BIND_FIXTURE=1 -e "NETWORK_SYSTEMD_CASE=$network_case" --mount "type=bind,src=$fixture,dst=/fixtures,readonly" --mount "type=bind,src=$fixture/coord,dst=/coord" "$image" >/dev/null
fi
docker run -d --platform "linux/$arch" --name "$container" "${network_args[@]}" --privileged --cgroupns=private \
 --tmpfs /run --tmpfs /run/lock --tmpfs /tmp \
 -e CTLVPS_BIND_FIXTURE=1 -e CTLVPS_SYSTEMD_NETWORK_FIXTURE=1 -e CTLVPS_NETWORK_CONTRACT_TEST=1 \
 -e "NETWORK_TEST_CLIENT=$client" \
 -e "NETWORK_SYSTEMD_CASE=$network_case" \
 -e "MITA_TEST_TRANSPORT=${MITA_TEST_TRANSPORT:-}" \
 -e "MIERU_TEST_CLIENT=${MIERU_TEST_CLIENT:-}" \
 --mount "type=bind,src=$fixture,dst=/fixtures,readonly" --mount "type=bind,src=$fixture/coord,dst=/coord" "$image" >/dev/null
for ((attempt=0;attempt<30;attempt++)); do
 if docker exec "$container" test -d /run/systemd/system; then break; fi
 sleep 1
done
# Wait for boot-time tmpfiles cleanup before creating fixture CA/temp files.
# Degraded is expected in a container; an unfinished boot or timeout is not.
docker exec "$container" timeout 45s sh -c 'systemctl is-system-running --wait; boot_result=$?; [ "$boot_result" -le 1 ]' >/dev/null
if [[ "$network_case" == transit || "$network_case" == transit-ss2022 ]]; then
 docker exec "$landing_container" timeout 45s sh -c 'systemctl is-system-running --wait; boot_result=$?; [ "$boot_result" -le 1 ]' >/dev/null
 docker exec "$landing_container" timeout 600s /fixtures/integration transit-landing > "$fixture/landing.log" 2>&1 &
fi
if [[ "$network_case" != native-subscriptions && "$network_case" != subscriptions && "$network_case" != forward-private && "$network_case" != forward-dns && "$network_case" != forward-socks && "$network_case" != forward-ssh && "$network_case" != forward-wireguard && "$network_case" != forward-udp && "$network_case" != reality && "$network_case" != listen && "$network_case" != protocols && "$network_case" != transit && "$network_case" != transit-ss2022 && "$network_case" != forward && "$network_case" != ssh && "$network_case" != wireguard && "$network_case" != mita && "$network_case" != wgaccess ]]; then docker exec "$container" /fixtures/transport.test -test.run '^TestNetworkContractContainer$' -test.v; fi
if ! docker exec "$container" timeout 600s /fixtures/integration; then
 if [[ "$network_case" == transit || "$network_case" == transit-ss2022 ]]; then cat "$fixture/landing.log"; docker exec "$landing_container" journalctl --no-pager -n 60 -u ctlvps-agent.service -u ctlvps-singbox.service; fi
 docker exec "$container" journalctl --no-pager -n 100 -u ctlvps-agent.service -u ctlvps-singbox.service -u ctlvps-proxy-guard.service -u 'ctlvps-mita@*.service'
 exit 1
fi
