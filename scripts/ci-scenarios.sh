#!/usr/bin/env bash
# Fixed CI scenarios; no host credentials or real VPSes are used.
set -euo pipefail
cd "$(dirname "$0")/.."
category=${1:?category required}
scenario=${2:?scenario required}
export NETWORK_TEST_ARCH=amd64 ALPINE_TEST_ARCH=amd64 ISSUE_TEST_ARCH=amd64
fixtures="$PWD/.ci/fixtures"
assets=release/v0.1.999-ci
printf 'CI scenario: %s/%s\n' "$category" "$scenario"
case "$category" in
 race)
  case "$scenario" in
   store|api) packages=("./internal/$scenario");;
   support) packages=(./internal/auth ./internal/geoip ./internal/maintenance ./internal/agent ./internal/secureupdate ./internal/safehttp ./internal/backup ./internal/connlog);;
   *) exit 2;;
  esac
  CTLVPS_TEST_LOG=".ci/race-$scenario.ndjson" python3 scripts/ci-go-test.py -race -count=1 -timeout=8m "${packages[@]}"
  ;;
 integration)
  case "$scenario" in
   install) bash scripts/test-install-container.sh "$assets"; bash scripts/test-uninstall-container.sh;;
   maintenance) bash scripts/test-maintenance-container.sh "$assets";;
   meter) bash scripts/test-meter-container.sh;;
   browser)
    npm install --prefix "$RUNNER_TEMP/browser" --no-save playwright@1.62.1
    "$RUNNER_TEMP/browser/node_modules/.bin/playwright" install --with-deps chromium
    PLAYWRIGHT_MODULE_DIR="$RUNNER_TEMP/browser/node_modules" CTLVPS_TEST_BINARY="$PWD/bin/ctlvpsd-linux-amd64" node scripts/security-browser.mjs
    go test ./internal/proxynode -run '^$' -fuzz FuzzParseAny -fuzztime 20s -parallel 2
    ;;
   docker) bash scripts/test-docker.sh;;
   regressions) bash scripts/test-issue-regressions-container.sh;;
   *) exit 2;;
  esac
  ;;
 network|soak)
  export SINGBOX_BIN="$fixtures/sing-box" SSLOCAL_BIN="$fixtures/sslocal"
  case "$category/$scenario" in
   network/managed) cases=(transit-ss2022 forward subscriptions);;
   network/direct|network/ssh|network/wireguard) cases=("$scenario"); export NETWORK_TEST_CLIENT=shadowsocks-rust;;
   network/udp) cases=(forward-udp forward-both forward-udp6 forward-socks-udp forward-wireguard-udp);;
   soak/udp) cases=(forward-stress); export UDP_STRESS_SECONDS=${UDP_STRESS_SECONDS:-600}
    case "$UDP_STRESS_SECONDS" in 60|600) ;; *) echo "unsupported CI soak duration" >&2; exit 2;; esac;;
   *) exit 2;;
  esac
  needed=(sing-box)
  if [[ ${NETWORK_TEST_CLIENT:-} == shadowsocks-rust ]]; then needed+=(sslocal); fi
  python3 scripts/fetch-network-test-fixtures.py "$fixtures" --arch amd64 --catalog "$fixtures/catalog.json" --only "${needed[@]}"
  for network_case in "${cases[@]}"; do
   printf 'Starting network scenario: %s\n' "$network_case"
   NETWORK_SYSTEMD_CASE="$network_case" bash scripts/test-network-systemd-container.sh
  done
  ;;
 alpine)
  export SINGBOX_MUSL_BIN="$fixtures/sing-box-musl" MITA_BIN="$fixtures/mita" MIERU_BIN="$fixtures/mieru"
  if [[ "$scenario" != install ]]; then
   needed=(sing-box-musl)
   if [[ "$scenario" == protocols ]]; then needed+=(mita mieru); fi
   python3 scripts/fetch-network-test-fixtures.py "$fixtures" --arch amd64 --catalog "$fixtures/catalog.json" --only "${needed[@]}"
  fi
  case "$scenario" in
   install) bash scripts/test-alpine-installer-container.sh;;
   memory) ALPINE_TEST_MEMORY='256m 512m' bash scripts/test-alpine-container.sh;;
   protocols)
    ALPINE_TEST_CASE=mita ALPINE_TEST_MEMORY=512m bash scripts/test-alpine-container.sh
    ALPINE_TEST_CASE=udp ALPINE_TEST_MEMORY=512m bash scripts/test-alpine-container.sh
    ;;
   maintenance)
    MAINTENANCE_INIT=openrc bash scripts/test-maintenance-container.sh
    MAINTENANCE_INIT=openrc ALPINE_TEST_PURGE=1 bash scripts/test-maintenance-container.sh
    ;;
   *) exit 2;;
  esac
  ;;
 *) exit 2;;
esac
printf 'PASS CI scenario: %s/%s\n' "$category" "$scenario"
