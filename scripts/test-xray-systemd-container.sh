#!/usr/bin/env bash
# Run on a native Linux AMD64 Docker host. An optional local official zip
# avoids the download and is subject to the same digest check.
set -euo pipefail
if (( $# > 1 )); then echo "usage: $0 [Xray-linux-64.zip]" >&2; exit 2; fi
if [[ $(uname -s) != Linux || $(uname -m) != x86_64 ]]; then
  echo 'Xray systemd acceptance requires a native Linux AMD64 host' >&2
  exit 1
fi
case $(docker info --format '{{.OSType}}/{{.Architecture}}') in
  linux/x86_64|linux/amd64) ;;
  *) echo 'Xray systemd acceptance requires a Linux AMD64 Docker daemon' >&2; exit 1 ;;
esac
archive=${1:+$(realpath -- "$1")}
cd "$(dirname "$0")/.."
work=$(mktemp -d)
fixture="$work/fixture"
image="vpsct-xray-test:$(basename "$work" | tr '[:upper:]' '[:lower:]')"
container=''
cleanup() {
  if [[ -n "$container" ]]; then docker rm -fv "$container" >/dev/null || true; fi
  docker image rm "$image" >/dev/null 2>&1 || true
  rm -rf -- "$work"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
mkdir -m 0755 "$fixture"
zip="$fixture/Xray-linux-64.zip"
if [[ -n "$archive" ]]; then
  cp -- "$archive" "$zip"
else
  curl --fail --location --silent --show-error --proto '=https' --proto-redir '=https' \
    --retry 3 --max-time 180 -o "$zip" \
    https://github.com/XTLS/Xray-core/releases/download/v25.12.8/Xray-linux-64.zip
fi
# Both tests independently verify these bytes against agentproto.PinnedXray().
printf '%s  %s\n' 97f20fed49750c24fc389c2946549ba2a374907e07e9adb2ce75799dd80088d9 "$zip" | sha256sum --check
chmod 0644 "$zip"
export CGO_ENABLED=0 GOOS=linux GOARCH=amd64
go build -mod=readonly -o "$fixture/ctlvps-agent" ./cmd/ctlvps-agent
go test -mod=readonly -c -o "$fixture/core.test" ./internal/core
chmod 0755 "$fixture/core.test"

docker build --platform linux/amd64 -t "$image" - < scripts/maintenance-test/Dockerfile
container=$(docker create --platform linux/amd64 --network none --hostname vpsct-xray-test \
  --add-host origin.example.test:127.0.0.2 --sysctl net.ipv4.ip_unprivileged_port_start=0 \
  --privileged --cgroupns=private --memory=1g --memory-swap=1g \
  --tmpfs /run:mode=0755 --tmpfs /run/lock --tmpfs /tmp:exec,mode=1777 \
  --mount "type=bind,src=$fixture,dst=/fixtures,readonly" \
  -e VPSCT_TEST_XRAY_ARCHIVE=/fixtures/Xray-linux-64.zip "$image")
docker start "$container" >/dev/null
bash scripts/wait-systemd-container.sh "$container"
docker exec "$container" install -o root -g root -m 0755 /fixtures/ctlvps-agent /usr/local/bin/ctlvps-agent

docker exec --user 65534:65534 "$container" /fixtures/core.test -test.v -test.count=1 \
  -test.timeout=2m -test.run '^TestOfficialXrayXHTTPTransport$' | tee "$work/transport.log"
for mode in auto stream-one packet-up stream-up; do
  grep -Eq "^[[:space:]]*--- PASS: TestOfficialXrayXHTTPTransport/$mode " "$work/transport.log"
done
# As in the network systemd fixture, keep the exec process out of the cgroup
# root so that systemd can delegate the v2 memory controller to child slices.
docker exec -e VPSCT_TEST_XRAY_SYSTEMD=1 "$container" sh -ec '
  echo "$$" > /sys/fs/cgroup/init.scope/cgroup.procs
  echo +memory > /sys/fs/cgroup/cgroup.subtree_control
  exec /fixtures/core.test -test.v -test.count=1 -test.timeout=2m -test.run "^TestIsolatedXraySystemdRollback$"
' | tee "$work/rollback.log"
grep -Eq '^--- PASS: TestIsolatedXraySystemdRollback ' "$work/rollback.log"
