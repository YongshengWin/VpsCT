#!/bin/sh
# All services, volumes and backup files belong to a disposable Compose project.
set -eu
root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
image=${CTLVPS_COMPOSE_TEST_IMAGE:-ctlvps-container-test:local}
work=$(mktemp -d)
COMPOSE_PROJECT_NAME="ctlvps-compose-test-$(basename "$work" | tr "[:upper:]." "[:lower:]-")"
export COMPOSE_PROJECT_NAME
bad_image="ctlvps-compose-bad:$COMPOSE_PROJECT_NAME"
export CTLVPS_IMAGE="$image"
export CTLVPS_SITE_URL=https://panel.example.test
unset COMPOSE_FILE COMPOSE_PROFILES
mkdir "$work/deploy"
cp "$root/deploy/compose-upgrade.sh" "$root/deploy/docker-compose.yml" "$work/deploy/"
# Avoid the production container name and port; exercise the rest of the real config.
sed -i.bak '/container_name:/d; /ports:/{N;d;}' "$work/deploy/docker-compose.yml"
rm "$work/deploy/docker-compose.yml.bak"
cd "$work/deploy"
compose() { docker compose "$@"; }
cleanup() {
  if [ -d backups ]; then
    for file in backups/*/anchor; do
      [ ! -f "$file" ] || docker rm -v "$(cat "$file")" >/dev/null 2>&1 || true
    done
    for file in backups/*/rollback-tag; do
      [ ! -f "$file" ] || docker image rm "$(cat "$file")" >/dev/null 2>&1 || true
    done
  fi
  compose down -v --remove-orphans >/dev/null 2>&1 || true
  docker image rm "ctlvps-compose-built:$COMPOSE_PROJECT_NAME" >/dev/null 2>&1 || true
  docker image rm "$bad_image" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
compose up -d --no-build --pull never
cid=$(compose ps -q ctlvpsd)
for _attempt in $(seq 1 60); do
  if docker exec "$cid" wget -qO- 'http://127.0.0.1:8080/healthz?ready=1' >/dev/null 2>&1; then break; fi
  sleep 1
done
docker exec "$cid" test -s /data/setup-token
docker exec "$cid" /bin/sh -c 'printf original > /data/upgrade-marker'
key_before=$(docker exec "$cid" sha256sum /keys/secrets.key)
token_before=$(docker exec "$cid" sha256sum /data/setup-token)
# Run the actual POSIX host script, also runnable under Alpine BusyBox sh.
run_upgrade() { sh ./compose-upgrade.sh "$@"; }
run_upgrade upgrade "$image"
cid=$(compose ps -q ctlvpsd)
[ "$(docker exec "$cid" cat /data/upgrade-marker)" = original ]
[ "$(docker exec "$cid" sha256sum /keys/secrets.key)" = "$key_before" ]
[ "$(docker exec "$cid" sha256sum /data/setup-token)" = "$token_before" ]
# Exercise the default source-build path without rebuilding Go and Node again.
printf 'FROM %s\nLABEL io.vpsct.compose-test=upgrade\n' "$image" > "$work/Dockerfile"
export CTLVPS_IMAGE="ctlvps-compose-built:$COMPOSE_PROJECT_NAME"
run_upgrade upgrade
cid=$(compose ps -q ctlvpsd)
[ "$(docker exec "$cid" sha256sum /keys/secrets.key)" = "$key_before" ]
[ "$(docker exec "$cid" sha256sum /data/setup-token)" = "$token_before" ]
# Create a real account after testing the unconsumed bootstrap capability.
# No token, password response or session cookie is printed.
docker exec -i "$cid" /bin/sh <<'SETUP'
set -eu
token=$(cat /data/setup-token)
payload=$(printf '{"username":"compose-test","password":"Disposable-test-password-42","setup_token":"%s"}' "$token")
wget -qO /dev/null --header='Host: panel.example.test' --header='Origin: https://panel.example.test' --header='Content-Type: application/json' --post-data="$payload" http://127.0.0.1:8080/api/v1/auth/setup
test ! -f /data/setup-token
SETUP
# A real candidate mutates data and then fails. Rollback must restore both volumes.
cat > "$work/Dockerfile.bad" <<EOF_BAD
FROM $image
ENTRYPOINT ["/bin/sh", "-c", "echo failed > /data/upgrade-marker; echo damaged > /data/ctlvps.db; echo damaged > /keys/secrets.key; exit 1"]
EOF_BAD
docker build -f "$work/Dockerfile.bad" -t "$bad_image" "$work" >/dev/null
set +e
run_upgrade upgrade "$bad_image"
result=$?
set -e
[ "$result" -eq 20 ]
cid=$(compose ps -q ctlvpsd)
[ "$(docker exec "$cid" cat /data/upgrade-marker)" = original ]
[ "$(docker exec "$cid" sha256sum /keys/secrets.key)" = "$key_before" ]
docker exec "$cid" test ! -f /data/setup-token
docker exec "$cid" wget -qO /dev/null --header='Host: panel.example.test' --header='Origin: https://panel.example.test' --header='Content-Type: application/json' --post-data='{"username":"compose-test","password":"Disposable-test-password-42"}' http://127.0.0.1:8080/api/v1/auth/login
backup=$(find "$work/deploy/backups" -name complete | sort | tail -1)
backup=${backup%/complete}
# Explicit rollback is repeatable and checks archive integrity before stopping.
(export COMPOSE_PROJECT_NAME=ctlvps-unrelated-recovery-test; run_upgrade rollback "$backup")
printf x >> "$backup/snapshot.tar"
set +e
run_upgrade rollback "$backup"
result=$?
set -e
[ "$result" -ne 0 ]
cid=$(compose ps -q ctlvpsd)
docker exec "$cid" wget -qO- 'http://127.0.0.1:8080/healthz?ready=1' >/dev/null
# Configuration changes are refused before stopping or touching application data.
export CTLVPS_SITE_URL=https://changed.example.test
set +e
run_upgrade upgrade "$image"
result=$?
set -e
[ "$result" -ne 0 ]
[ "$(docker inspect --format '{{.State.Running}}' "$cid")" = true ]
echo 'Compose upgrade passed: persistence, data+key rollback, repeatable recovery, checksum and config guards.'
