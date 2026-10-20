#!/usr/bin/env bash
# A created /run/systemd directory and exit 1 do not imply a ready system bus.
set -euo pipefail
container=${1:?disposable test container required}
docker exec "$container" timeout 45s sh -c '
 while :; do
  state=$(systemctl is-system-running 2>/dev/null || true)
  case "$state" in running|degraded) exit 0;; esac
  sleep 0.2
 done
'
