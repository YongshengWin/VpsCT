#!/bin/sh
# Host-side Compose maintenance; compatible with Alpine BusyBox /bin/sh.
set -eu
umask 077
cd "$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
usage() {
  echo '用法: sh compose-upgrade.sh upgrade [本地镜像] | rollback 备份目录' >&2
  exit 2
}
fail() { echo "$*" >&2; exit 1; }
[ "$#" -ge 1 ] && [ "$#" -le 2 ] || usage
mode=$1
case "$mode" in upgrade|rollback) ;; *) usage ;; esac
[ "$mode" != rollback ] || [ "$#" -eq 2 ] || usage
command -v docker >/dev/null || fail '需要 Docker 和 Docker Compose v2'
command -v sha256sum >/dev/null || fail '需要 sha256sum（Alpine BusyBox 已提供）'
docker compose version >/dev/null
# Only the local Docker engine owns these containers and volumes.
case "${DOCKER_HOST:-}" in ''|unix://*) ;; *) fail '只支持本机 Docker 引擎' ;; esac
endpoint=$(docker context inspect --format '{{.Endpoints.docker.Host}}')
case "$endpoint" in unix://*) ;; *) fail '只支持本机 Docker context' ;; esac
mkdir .compose-upgrade.lock 2>/dev/null || fail '已有维护任务；中断后请先核对现场再清理 deploy/.compose-upgrade.lock'
probe=
stopped=0
finished=0
backup=
cleanup() {
  [ -z "$probe" ] || docker rm -fv "$probe" >/dev/null 2>&1 || true
  if [ "$stopped" = 1 ] && [ "$finished" = 0 ]; then
    if [ ! -f "$backup/complete" ]; then
      docker start "$old" >/dev/null 2>&1 || true
      echo "快照尚未完成，已尝试重启原容器；请核对状态。" >&2
    else
      echo "维护中断，恢复点：$backup；请执行 rollback 恢复。" >&2
    fi
  fi
  rmdir .compose-upgrade.lock
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
compose() { docker compose "$@"; }
# Health is deliberately a deep local check, including encrypted data and agents.
ready() {
  attempt=0
  stable=0
  while [ "$attempt" -lt 60 ]; do
    state=$(docker inspect --format '{{.State.Status}}/{{.RestartCount}}' "$1") || return 1
    [ "$state" = running/0 ] || return 1
    if docker exec "$1" wget -T 8 -qO- 'http://127.0.0.1:8080/healthz?ready=1' >/dev/null 2>&1; then
      stable=$((stable + 1))
      [ "$stable" -lt 3 ] || return 0
    else
      stable=0
    fi
    attempt=$((attempt + 1))
    sleep 1
  done
  return 1
}
helper() {
  docker run --rm -i --network none --read-only --security-opt no-new-privileges \
    --volumes-from "$anchor" --entrypoint /bin/sh "$old_image" -c "$1"
}
restore() {
  # The backup directory is trusted local administrator input, never webpage input.
  (cd "$backup" && sha256sum -c snapshot.sha256 >/dev/null) || return 1
  docker inspect "$anchor" >/dev/null || return 1
  docker image inspect "$old_image" >/dev/null || return 1
  docker compose -p "$project" -f "$backup/compose.yaml" -f "$backup/old-image.yaml" stop -t 30 ctlvpsd || return 1
  # Keep the failed data separately. Never overwrite the only recovery point.
  helper 'tar -C / -cf - data keys' > "$backup/failed-$(date -u +%Y%m%dT%H%M%SZ)-$$.tar" || return 1
  helper 'find /data /keys -mindepth 1 -maxdepth 1 -exec rm -rf -- {} + && tar -C / -xpf -' < "$backup/snapshot.tar" || return 1
  docker compose -p "$project" -f "$backup/compose.yaml" -f "$backup/old-image.yaml" up -d --no-build --pull never --no-deps --force-recreate ctlvpsd || return 1
  restored=$(docker compose -p "$project" -f "$backup/compose.yaml" -f "$backup/old-image.yaml" ps -q ctlvpsd) || return 1
  ready "$restored"
}
if [ "$mode" = rollback ]; then
  backup=$(CDPATH='' cd -- "$2" && pwd)
  [ -f "$backup/complete" ] || fail '备份不完整，拒绝恢复'
  project=$(cat "$backup/project")
  anchor=$(cat "$backup/anchor")
  old_image=$(cat "$backup/old-image")
  restore || fail "恢复失败，保留停服现场与备份：$backup"
  echo '旧镜像和配套数据已恢复；后续升级前请保留此备份。'
  exit 0
fi
old=$(compose ps -q ctlvpsd)
[ -n "$old" ] || fail '未找到运行中的 ctlvpsd；首次安装请使用 docker compose up -d --build'
[ "$(docker inspect --format '{{.State.Running}}' "$old")" = true ] || fail '控制端必须处于运行状态'
# Restrict automated restore to the stock two named volumes. No host path removal.
mounts=$(docker inspect --format '{{range .Mounts}}{{.Type}} {{.Destination}} {{.RW}}{{println}}{{end}}' "$old" | sed '/^$/d' | sort)
[ "$mounts" = "$(printf 'volume /data true\nvolume /keys true\n' | sort)" ] || fail '仅支持 /data、/keys 两个命名卷；自定义挂载请手动升级'
project=$(docker inspect --format '{{index .Config.Labels "com.docker.compose.project"}}' "$old")
old_image=$(docker inspect --format '{{.Image}}' "$old")
if [ "$#" -eq 2 ]; then
  docker image inspect "$2" >/dev/null || fail '请先获取并校验目标本地镜像'
  target_image=$(docker image inspect --format '{{.Id}}' "$2")
else
  compose build ctlvpsd
  target_image=
fi
backup_root="$(pwd)/backups"
mkdir -p "$backup_root"
backup="$backup_root/$(date -u +%Y%m%dT%H%M%SZ)-$$"
mkdir "$backup"
compose config > "$backup/compose.yaml"
printf '%s\n' "$project" > "$backup/project"
printf 'services:\n  ctlvpsd:\n    image: %s\n' "$old_image" > "$backup/old-image.yaml"
# Preserve a reference even if a later local build replaces its usual tag.
rollback_tag="ctlvps-rollback:${project}-$(date -u +%Y%m%dT%H%M%SZ)-$$"
docker tag "$old_image" "$rollback_tag"
printf '%s\n' "$old_image" > "$backup/old-image"
printf '%s\n' "$rollback_tag" > "$backup/rollback-tag"
if [ -n "$target_image" ]; then
  printf 'services:\n  ctlvpsd:\n    image: %s\n' "$target_image" > "$backup/new-image.yaml"
else
  printf 'services:\n  ctlvpsd: {}\n' > "$backup/new-image.yaml"
fi
# Create a sleeping probe first: no database access before mount/env comparison.
probe="ctlvps-compose-probe-${project}-$$"
docker compose -p "$project" -f "$backup/compose.yaml" -f "$backup/new-image.yaml" run -d --no-deps --name "$probe" --entrypoint /bin/sh ctlvpsd -c 'sleep 900' >/dev/null
for container in "$old" "$probe"; do
  docker inspect --format '{{range .Mounts}}{{.Type}} {{.Name}} {{.Source}} {{.Destination}} {{.RW}}{{println}}{{end}}' "$container" | sort > "$backup/$container.mounts"
  docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "$container" | sort > "$backup/$container.env"
done
cmp -s "$backup/$old.mounts" "$backup/$probe.mounts" || fail '目标数据卷发生变化，拒绝升级'
cmp -s "$backup/$old.env" "$backup/$probe.env" || fail '环境配置发生变化，请先单独完成配置变更再升级'
# Validate the fixed paths expected by backup/restore even for older images.
docker exec "$probe" /bin/sh -c '[ "$CTLVPS_DATA_DIR" = /data ] && [ "$CTLVPS_SECRETS_KEY_FILE" = /keys/secrets.key ] && [ "$CTLVPS_LISTEN" = :8080 ] && [ "$(id -u)" = 10001 ]' || fail '目标不是标准 Compose 布局'
target_image=$(docker inspect --format '{{.Image}}' "$probe")
printf 'services:\n  ctlvpsd:\n    image: %s\n' "$target_image" > "$backup/new-image.yaml"
docker rm -fv "$probe" >/dev/null
probe=
anchor="ctlvps-compose-backup-${project}-$(date -u +%Y%m%dT%H%M%SZ)-$$"
docker create --name "$anchor" --network none --read-only --volumes-from "$old" --entrypoint /bin/sh "$old_image" -c ':' >/dev/null
printf '%s\n' "$anchor" > "$backup/anchor"
echo "即将停服备份；恢复点：$backup"
stopped=1
compose stop -t 30 ctlvpsd
# No schema migration can run until the complete data + key snapshot is closed.
if ! helper 'tar -C / -cf - data keys' > "$backup/snapshot.tar"; then
  docker start "$old" >/dev/null
  fail '快照失败，已尝试启动原容器'
fi
(cd "$backup" && sha256sum snapshot.tar compose.yaml old-image.yaml old-image anchor rollback-tag project > snapshot.sha256)
touch "$backup/complete"
echo '快照完成，开始切换镜像。'
if docker compose -p "$project" -f "$backup/compose.yaml" -f "$backup/new-image.yaml" up -d --no-build --pull never --no-deps --force-recreate ctlvpsd; then
  current=$(docker compose -p "$project" -f "$backup/compose.yaml" -f "$backup/new-image.yaml" ps -q ctlvpsd)
  if ready "$current"; then
    finished=1
    echo "升级就绪；恢复点保留在：$backup"
    exit 0
  fi
fi
echo '升级未就绪，正在恢复旧镜像与配套数据。' >&2
restore || fail "自动恢复失败，保留停服现场；请用 rollback 检查恢复：$backup"
finished=1
echo '已回退；本次升级失败（退出码 20）。' >&2
exit 20
