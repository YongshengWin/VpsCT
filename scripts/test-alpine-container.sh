#!/usr/bin/env bash
# Real OpenRC/cgroup/sandbox tests in disposable, network-none containers.
# Supply an independently verified official sing-box 1.14.1 musl binary.
set -euo pipefail
cd "$(dirname "$0")/.."
test_case=${ALPINE_TEST_CASE:-full}
case "$test_case" in full|mita|maintenance|udp) ;; *) echo 'unknown Alpine test case' >&2; exit 1;; esac
binary=${SINGBOX_MUSL_BIN:?Set SINGBOX_MUSL_BIN to a verified official sing-box 1.14.1 musl binary}
[[ -f "$binary" ]] || { echo 'SINGBOX_MUSL_BIN is not a regular file' >&2; exit 1; }
fixture=$(mktemp -d)
chmod 0755 "$fixture"
container="ctlvps-alpine-test-$$"
cleanup() { docker rm -f "$container" >/dev/null 2>&1 || true; rm -rf -- "$fixture"; }
trap cleanup EXIT
arch=${ALPINE_TEST_ARCH:-$(docker info --format '{{.Architecture}}')}
case "$arch" in aarch64|arm64) arch=arm64;; x86_64|amd64) arch=amd64;; *) echo 'unsupported Docker architecture' >&2; exit 1;; esac
export CGO_ENABLED=0 GOOS=linux GOARCH="$arch"
go build -o "$fixture/ctlvps-agent" ./cmd/ctlvps-agent
go build -o "$fixture/integration" ./scripts/alpine-test
if [[ "$test_case" == udp ]]; then
 go build -o "$fixture/network-integration" ./scripts/network-systemd-test
 go build -o "$fixture/probe" ./scripts/network-bind-test
fi
go test -c -o "$fixture/nft.test" ./internal/nft
go test -c -o "$fixture/agent.test" ./internal/agent
go test -c -o "$fixture/proxysandbox.test" ./internal/proxysandbox
cp -- "$binary" "$fixture/sing-box"
if [[ "$test_case" == mita ]]; then
 cp -- "${MITA_BIN:?Set MITA_BIN to verified official mita 3.37.0}" "$fixture/mita"
 cp -- "${MIERU_BIN:?Set MIERU_BIN to verified official mieru 3.37.0}" "$fixture/mieru"
fi
if [[ "$test_case" == maintenance ]]; then
 go test -c -o "$fixture/maintenance.test" ./internal/maintenance
fi
security=(--privileged)
if [[ -n ${ALPINE_TEST_SECCOMP:-} ]]; then
 security=(--cap-add SYS_ADMIN --cap-add NET_ADMIN --cap-add SYS_RESOURCE --cap-add SYS_PTRACE --cap-add BPF --cap-add PERFMON --security-opt "seccomp=$ALPINE_TEST_SECCOMP")
fi
image="ctlvps-alpine-test:$arch"
docker build --platform "linux/$arch" -t "$image" scripts/alpine-test
# 128 MiB is an experimental stress case, not a supported capacity promise.
for memory in ${ALPINE_TEST_MEMORY:-256m 128m}; do
 case "$memory" in 128m|256m|512m) ;; *) echo 'memory case must be 128m, 256m or 512m' >&2; exit 1;; esac
 docker run -d --platform "linux/$arch" --name "$container" --network none \
   "${security[@]}" --cgroupns=private --memory "$memory" --memory-swap "$memory" \
   --tmpfs /run --tmpfs /tmp --tmpfs /run/lock \
   --mount "type=bind,src=$fixture,dst=/fixtures,readonly" \
   --mount "type=bind,src=$PWD,dst=/work,readonly" \
   -e CTLVPS_ALPINE_FIXTURE=1 -e CTLVPS_KERNEL_TEST=1 -e "CTLVPS_TEST_PURGE=${ALPINE_TEST_PURGE:-0}" "$image" >/dev/null
 for ((attempt=0;attempt<30;attempt++)); do
   if docker exec "$container" test -f /run/openrc/softlevel; then break; fi
   sleep 1
 done
 docker exec "$container" sh -ec 'mount -o remount,rw /sys/fs/cgroup; python3 /work/scripts/alpine-test/prepare.py; install -m 0755 /fixtures/ctlvps-agent /usr/local/bin/ctlvps-agent; /usr/local/bin/ctlvps-agent openrc-check'
 docker exec "$container" /fixtures/sing-box version | head -n 1 | grep -Fx 'sing-box version 1.14.1'
 if [[ "$test_case" == udp ]]; then
   docker exec -e CTLVPS_SYSTEMD_NETWORK_FIXTURE=1 -e CTLVPS_BIND_FIXTURE=1 -e NETWORK_SYSTEMD_CASE=forward-udp "$container" timeout 600s /fixtures/network-integration
   docker exec "$container" awk '$1 == "oom_kill" && $2 != 0 { exit 1 }' /sys/fs/cgroup/memory.events
   docker inspect --format 'container OOMKilled={{.State.OOMKilled}} memory={{.HostConfig.Memory}}' "$container"
   docker rm -f "$container" >/dev/null
   continue
 fi
 if [[ "$test_case" == mita ]]; then
   docker exec "$container" /fixtures/mita version | grep -Fx '3.37.0'
   docker exec "$container" timeout 240s /fixtures/integration mita
   docker rm -f "$container" >/dev/null
   continue
 fi
 if [[ "$test_case" == maintenance ]]; then
   docker exec "$container" timeout 120s /fixtures/integration agent-lifecycle
   docker exec "$container" timeout 240s /fixtures/maintenance.test -test.run '^TestKernelOpenRCMaintenance' -test.v
   docker rm -f "$container" >/dev/null
   continue
 fi
 if [[ ${ALPINE_TEST_READY_ONLY:-0} == 1 ]]; then
   docker exec "$container" timeout 120s /fixtures/integration agent-lifecycle
   docker inspect --format 'container OOMKilled={{.State.OOMKilled}} memory={{.HostConfig.Memory}}' "$container"
   docker rm -f "$container" >/dev/null
   continue
 fi
 if ! docker exec "$container" timeout 300s /fixtures/integration; then
   docker exec "$container" sh -c 'rc-status -a; cat /var/log/ctlvps/sing-box.log; cat /sys/fs/cgroup/memory.events' || true
   exit 1
 fi
 docker exec "$container" /fixtures/proxysandbox.test -test.run '^TestKernelSocketSandbox$' -test.v
 # Reuse the exact packet/conntrack and complete origin-accounting assertions.
 for test_name in TestKernelNodeAccounting TestKernelEgressBoundary TestKernelNAT; do
   docker exec "$container" unshare --net /fixtures/nft.test -test.run "^${test_name}$" -test.v
 done
 docker exec -e CTLVPS_NAT_AGENT_FIXTURE=1 "$container" unshare --net /fixtures/agent.test -test.run '^TestKernelControllerNAT$' -test.v
 docker inspect --format 'container OOMKilled={{.State.OOMKilled}} memory={{.HostConfig.Memory}}' "$container"
 docker rm -f "$container" >/dev/null
done
