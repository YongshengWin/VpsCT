#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
image=ctlvps-container-test:local
name="ctlvps-smoke-$$"
volume="ctlvps-smoke-data-$$"
cleanup() {
  docker rm -fv "$name" >/dev/null 2>&1 || true
  docker volume rm "$volume" >/dev/null 2>&1 || true
}
trap cleanup EXIT
docker build -t "$image" .
docker volume create "$volume" >/dev/null
docker run -d --name "$name" --read-only --cap-drop ALL --security-opt no-new-privileges --tmpfs /tmp -e CTLVPS_SITE_URL=https://panel.example.test --mount "type=volume,src=$volume,dst=/data" "$image" >/dev/null
ready=0
for ((attempt = 0; attempt < 30; attempt++)); do
  if docker exec "$name" wget -qO- http://127.0.0.1:8080/healthz >/dev/null; then ready=1; break; fi
  sleep 1
done
[[ "$ready" == 1 ]] || { docker logs "$name"; exit 1; }
[[ "$(docker exec "$name" id -u)" != 0 ]]
docker exec "$name" test -s /data/setup-token
docker exec "$name" wget --header="Host: panel.example.test" -qO- http://127.0.0.1:8080/api/v1/auth/setup | python3 -c 'import json,sys; assert json.load(sys.stdin)["needs_setup"]'
# The bootstrap capability must survive a restart; print only hashes, not tokens.
before=$(docker exec "$name" sha256sum /data/setup-token)
docker restart "$name" >/dev/null
for ((attempt = 0; attempt < 30; attempt++)); do
  if docker exec "$name" wget -qO- http://127.0.0.1:8080/healthz >/dev/null; then break; fi
  sleep 1
done
[[ "$(docker exec "$name" sha256sum /data/setup-token)" == "$before" ]]
printf 'Docker smoke passed: unprivileged startup, writable volume, persistent setup capability.\n'
