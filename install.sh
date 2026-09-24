#!/usr/bin/env bash
# Release packaging replaces these two markers with this repository and tag.
# Source checkout usage: bash install.sh --repo OWNER/VpsCT ... (latest by default).
# Release assets default to their own tag; --version may override it.
set -euo pipefail

REPOSITORY='__REPOSITORY__'
VERSION='__VERSION__'
INSTALL_DIR=/opt/ctlvps
ENV_FILE=/etc/ctlvps/ctlvpsd.env
SERVICE_FILE=/etc/systemd/system/ctlvpsd.service
DOMAIN='' SITE_URL='' ASSETS_DIR=''
VERIFIER=/usr/local/libexec/ctlvps-verify
LISTEN=127.0.0.1:8080
NO_PROXY=0 UPDATE=0 AUTO_ROLLBACK=0
WORK='' BACKUP='' PREVIOUS='' STOPPED=0 SWITCHED=0 COMPLETE=0 RESERVATION='' NEW_RELEASE=''

die() { printf '错误：%s\n' "$*" >&2; exit 1; }
info() { printf '==> %s\n' "$*"; }

usage() {
  cat <<'EOF'
VpsCT 控制端安装器（Debian / Ubuntu，systemd，amd64 / arm64）

首次安装并配置 Caddy HTTPS：
  sudo bash install.sh --domain panel.example.com
使用已有 HTTPS 反向代理：
  sudo bash install.sh --site-url https://panel.example.com --no-proxy
升级（保留配置和账户，停服备份）：
  sudo bash install.sh --update

选项：
  --repo OWNER/VpsCT      下载来源（OWNER 为用户或组织；Release 附件内已自动填写）
  --version vX.Y.Z        可选，覆盖脚本默认版本；latest 在开始下载时解析一次
  --assets-dir DIRECTORY  使用本地发行附件（仍需 SHA256SUMS）
  --verifier PATH         恢复工具安装路径（默认 /usr/local/libexec/ctlvps-verify）
  --domain DOMAIN         自动安装并配置 Caddy；先设置 DNS 和 80/443 端口
  --site-url HTTPS_URL    已有反向代理提供的站点地址，需同时传 --no-proxy
  --no-proxy             保留用户现有的 HTTPS / 反向代理
  --update               升级安装器管理的现有安装
  --auto-rollback        升级启动失败时恢复旧版本和停服备份（网页升级使用）
  --help                 显示帮助

仅控制端在本机安装；agent 文件用于向后续接入的 VPS 分发。
EOF
}

valid_repo() { [[ "$1" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$ ]]; }
valid_version() { [[ "$1" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9][A-Za-z0-9.-]*)?$ ]]; }
version_order() { local value=${1#v}; printf '%s\n' "${value/-/\~}"; }
valid_domain() {
  [[ ${#1} -le 253 && "$1" == *.* && "$1" != *..* ]] || return 1
  local label
  local -a labels
  IFS=. read -r -a labels <<< "$1"
  for label in "${labels[@]}"; do
    [[ ${#label} -le 63 && "$label" =~ ^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?$ ]] || return 1
  done
  [[ "$1" != *. && ! "$1" =~ ^[0-9.]+$ ]]
}
valid_site_url() {
  [[ "$1" =~ ^https://[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?(:[0-9]{1,5})?$ ]] || return 1
  local authority=${1#https://} port
  if [[ "$authority" == *:* ]]; then
    port=${authority##*:}
    (( 10#$port >= 1 && 10#$port <= 65535 )) || return 1
  fi
}

download() {
  curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
    --retry 3 --connect-timeout 15 --max-time 600 "$1" -o "$2"
}

cleanup() {
  local status=$?
  trap - EXIT
  [[ -z "$RESERVATION" ]] || rm -f -- "$RESERVATION"
  if [[ "$COMPLETE" == 0 && "$SWITCHED" == 0 && -n "$NEW_RELEASE" && "$NEW_RELEASE" == "$INSTALL_DIR/releases/"* ]]; then
    rm -rf --one-file-system -- "$NEW_RELEASE"
  fi
  if [[ "$COMPLETE" == 0 && "$STOPPED" == 1 ]]; then
    if [[ "$SWITCHED" == 0 ]]; then
      systemctl start ctlvpsd || true
    else
      systemctl stop ctlvpsd || true
      if [[ "$AUTO_ROLLBACK" == 1 && -n "$BACKUP" && -f "$BACKUP/data.tar.gz.enc" ]] && restore_previous; then
        printf '升级失败，已恢复旧版本和升级前数据，健康检查通过。\n' >&2
        [[ -z "$WORK" ]] || rm -rf -- "$WORK"
        exit 20
      fi
      printf '安装未完成，控制端已停止。请查看 journalctl -u ctlvpsd。\n' >&2
      [[ -z "$BACKUP" ]] || printf '升级前备份：%s（恢复方法见 docs/operations.md）\n' "$BACKUP" >&2
    fi
  fi
  [[ -z "$WORK" ]] || rm -rf -- "$WORK"
  exit "$status"
}

reserve_update_space() {
  local data_size count capacity available total_inodes free_inodes reserve needed probe i
  command -v fallocate >/dev/null || die '缺少 fallocate，无法预留恢复空间；原服务保持运行'
  data_size=$(du --apparent-size -s -B1 --exclude="$INSTALL_DIR/data/backups" "$INSTALL_DIR/data" | awk '{print $1}')
  count=$(find "$INSTALL_DIR/data" -xdev -path "$INSTALL_DIR/data/backups" -prune -o -printf '.' | wc -c)
  read -r capacity available < <(df -B1 --output=size,avail "$INSTALL_DIR" | tail -n 1)
  read -r total_inodes free_inodes < <(df --output=itotal,iavail "$INSTALL_DIR" | tail -n 1)
  [[ "$data_size" =~ ^[0-9]+$ && "$capacity" =~ ^[0-9]+$ && "$available" =~ ^[0-9]+$ && "$free_inodes" =~ ^[0-9]+$ ]] || die '无法可靠计算恢复空间，原服务保持运行'
  reserve=$((capacity / 20)); (( reserve >= 268435456 )) || reserve=268435456
  # Plain snapshot, sealed snapshot, restore copy and WAL/migration headroom.
  needed=$((data_size * 4 + count * 8192 + 268435456))
  if [[ "$total_inodes" == 0 ]]; then
    # No fixed inode pool (e.g. some overlay filesystems); verify real creation.
    probe=$(mktemp -d "$INSTALL_DIR/.maintenance-inodes.XXXXXXXX")
    for ((i=0; i<16; i++)); do touch "$probe/$i" || die '无法分配维护文件，原服务保持运行'; done
    rm -rf -- "$probe"
    free_inodes=$((count * 2 + 256))
  fi
  (( available >= needed + reserve && free_inodes >= count * 2 + 256 )) || die '升级及恢复所需空间或 inode 不足，原服务保持运行；不会删除现有备份'
  RESERVATION=$(mktemp "$INSTALL_DIR/.maintenance-space.XXXXXXXX")
  chmod 0600 "$RESERVATION"
  fallocate -l "$needed" "$RESERVATION" || die '磁盘不支持可靠空间预留或配额不足，原服务保持运行'
}

restore_previous() {
  [[ "$PREVIOUS" == "$INSTALL_DIR/releases/"* && -x "$PREVIOUS/ctlvpsd" ]] || return 1
  [[ -f "$PREVIOUS/VERIFIED-SHA256" && ! -L "$PREVIOUS/VERIFIED-SHA256" ]] || return 1
  "$WORK/backup-tool" check-recovery "$WORK/recovery.json" || return 1
  [[ ! -L "$INSTALL_DIR/data" ]] || return 1
  rm -rf --one-file-system -- "$INSTALL_DIR/data" || return 1
  "$WORK/backup-tool" backup open /etc/ctlvps/secrets.key "$BACKUP/data.tar.gz.enc" "$WORK/restore.tar.gz" || return 1
  tar -xzf "$WORK/restore.tar.gz" -C "$INSTALL_DIR" || return 1
  cp -- "$BACKUP/ctlvpsd.env" "$ENV_FILE" || return 1
  if ! grep -q "^CTLVPS_SECRETS_KEY_FILE=" "$ENV_FILE"; then printf "\nCTLVPS_SECRETS_KEY_FILE=/etc/ctlvps/secrets.key\n" >> "$ENV_FILE"; fi
  cp -- "$BACKUP/ctlvpsd.service" "$SERVICE_FILE" || return 1
  ln -sfn "$PREVIOUS" "$INSTALL_DIR/current" || return 1
  if [[ -f "$BACKUP/ctlvps-maintenance.service" ]]; then
    cp -- "$BACKUP/ctlvps-maintenance.service" /etc/systemd/system/ctlvps-maintenance.service || return 1
  fi
  systemctl daemon-reload || return 1
  systemctl start ctlvpsd || return 1
  local attempt
  for ((attempt = 0; attempt < 30; attempt++)); do
    if systemctl is-active --quiet ctlvpsd && curl --noproxy '*' --fail --silent --max-time 2 "http://$LISTEN/healthz" > /dev/null; then
      if [[ -f "$BACKUP/ctlvps-maintenance.service" ]]; then systemctl restart ctlvps-maintenance || return 1; fi
      return 0
    fi
    sleep 1
  done
  return 1
}

check_environment() {
  [[ "$(uname -s)" == Linux ]] || die '仅支持 Linux'
  [[ "$(id -u)" == 0 ]] || die '请使用 root 或 sudo 运行'
  command -v systemctl >/dev/null || die '需要 systemd'
  [[ -d /run/systemd/system ]] || die 'systemd 必须正在运行'
  # Keep OS variables (notably VERSION) out of the installer's own state.
  local distro
  # shellcheck disable=SC1091
  distro=$(. /etc/os-release; printf '%s' "${ID:-}")
  case "$distro" in debian|ubuntu) ;; *) die '首版支持 Debian / Ubuntu；其他系统请手动部署' ;; esac
  case "$(uname -m)" in
    x86_64|amd64) ARCH=amd64 ;;
    aarch64|arm64) ARCH=arm64 ;;
    *) die '不支持的 CPU 架构' ;;
  esac
  command -v flock >/dev/null || die '缺少 flock，请安装 util-linux'
  exec 9>/run/lock/ctlvps-install.lock
  flock -n 9 || die '已有另一个安装器正在运行'
  if [[ "$UPDATE" == 1 ]]; then
    [[ -L "$INSTALL_DIR/current" && -f "$ENV_FILE" && -f "$INSTALL_DIR/REPOSITORY" ]] || die '找不到由安装器管理的现有安装'
    # A manually replaced entrypoint can make an otherwise successful update
    # keep running an old binary, even after current has moved to the new release.
    for entry in ctlvpsd agents; do
      [[ -L "$INSTALL_DIR/$entry" && "$(readlink "$INSTALL_DIR/$entry")" == "current/$entry" ]] ||
        die "安装入口 $INSTALL_DIR/$entry 已被替换；恢复 current/$entry 符号链接后再升级"
    done
    [[ -z "$DOMAIN" && -z "$SITE_URL" && "$NO_PROXY" == 0 ]] || die '升级保留站点配置，请勿同时指定域名或代理选项'
    PREVIOUS=$(readlink -f "$INSTALL_DIR/current")
    [[ "$PREVIOUS" == "$INSTALL_DIR/releases/"* && -x "$PREVIOUS/ctlvpsd" ]] || die '现有版本目录无效'
    if [[ "$AUTO_ROLLBACK" == 1 ]]; then
      [[ "$(sed -n 's/^CTLVPS_DATA_DIR=//p' "$ENV_FILE")" == /opt/ctlvps/data && ! -L "$INSTALL_DIR/data" ]] || die '自动恢复只支持默认数据目录'
      local mount
      while IFS= read -r mount; do
        [[ "$mount" != "$INSTALL_DIR/data" && "$mount" != "$INSTALL_DIR/data/"* ]] || die '数据目录内存在挂载点，不能自动恢复'
      done < <(findmnt -rn -o TARGET)
    fi
    SITE_URL=$(sed -n 's/^CTLVPS_SITE_URL=//p' "$ENV_FILE")
    valid_site_url "$SITE_URL" || die '现有站点 URL 无效，请检查环境文件'
    LISTEN=$(sed -n 's/^CTLVPS_LISTEN=//p' "$ENV_FILE")
    [[ "$LISTEN" =~ ^127\.0\.0\.1:([0-9]{1,5})$ ]] || die '安装器管理的后端必须绑定 127.0.0.1 的端口'
    local listen_port=${BASH_REMATCH[1]}
    (( 10#$listen_port >= 1 && 10#$listen_port <= 65535 )) || die '后端端口无效'
    if [[ "$REPOSITORY" == '__REPOSITORY__' ]]; then REPOSITORY=$(cat "$INSTALL_DIR/REPOSITORY"); fi
  else
    [[ ! -e "$INSTALL_DIR/current" && ! -e "$INSTALL_DIR/ctlvpsd" && ! -e "$ENV_FILE" && ! -e "$SERVICE_FILE" ]] || die '已有安装或配置；托管安装请使用 --update，手动安装请按迁移文档处理'
    [[ ! -d "$INSTALL_DIR/data" ]] || die '发现现有数据目录，拒绝覆盖；请手动迁移'
    command -v ss >/dev/null || die '缺少 ss，请安装 iproute2'
    [[ -z "$(ss -H -ltn '( sport = :8080 )')" ]] || die '控制端的 8080 端口被占用'
    if [[ "$NO_PROXY" == 1 ]]; then
      if [[ -n "$DOMAIN" ]] || ! valid_site_url "$SITE_URL"; then die '--no-proxy 需要 --site-url https://域名，可带端口'; fi
    else
      if [[ -n "$SITE_URL" ]] || ! valid_domain "$DOMAIN"; then die '请使用 --domain 域名，或 --site-url HTTPS_URL --no-proxy'; fi
      SITE_URL="https://$DOMAIN"
      if command -v caddy >/dev/null || [[ -e /etc/caddy/Caddyfile || -e /etc/apt/sources.list.d/caddy-stable.list ]]; then
        die '检测到现有 Caddy，请使用 --no-proxy 模式并自行添加站点'
      fi
      command -v ss >/dev/null || die '缺少 ss，请安装 iproute2'
      [[ -z "$(ss -H -ltn '( sport = :80 or sport = :443 )')" ]] || die '80/443 端口被占用，请使用 --no-proxy'
    fi
  fi
  valid_repo "$REPOSITORY" || die '请通过 --repo OWNER/VpsCT 指定已发布的 GitHub 仓库，OWNER 为实际用户或组织'
  [[ "$VERSION" != '__VERSION__' ]] || VERSION=latest
  [[ "$VERSION" == latest ]] || valid_version "$VERSION" || die '版本格式应为 vX.Y.Z 或 latest'
}

install_caddy() {
  info '通过 Caddy 官方软件源安装 HTTPS 代理'
  apt-get install -y --no-install-recommends debian-keyring debian-archive-keyring apt-transport-https gnupg
  download 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' "$WORK/caddy.key"
  download 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' "$WORK/caddy.list"
  gpg --batch --yes --dearmor -o "$WORK/caddy.gpg" "$WORK/caddy.key"
  install -m 0644 "$WORK/caddy.gpg" /usr/share/keyrings/caddy-stable-archive-keyring.gpg
  install -m 0644 "$WORK/caddy.list" /etc/apt/sources.list.d/caddy-stable.list
  apt-get update
  apt-get install -y --no-install-recommends caddy
  cat > "$WORK/Caddyfile" <<EOF
# Managed by VpsCT installer
$DOMAIN {
    reverse_proxy 127.0.0.1:8080
}
EOF
  caddy validate --config "$WORK/Caddyfile" --adapter caddyfile
  install -m 0644 "$WORK/Caddyfile" /etc/caddy/Caddyfile
  systemctl enable --now caddy
  systemctl reload caddy
}

main() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --help|-h) usage; return ;;
      --repo|--version|--domain|--site-url|--assets-dir|--verifier)
        [[ $# -ge 2 && -n "$2" && "$2" != --* ]] || die "$1 缺少参数"
        case "$1" in
          --repo) REPOSITORY=$2 ;; --version) VERSION=$2 ;; --domain) DOMAIN=$2 ;;
          --site-url) SITE_URL=${2%/} ;; --assets-dir) ASSETS_DIR=$2 ;; --verifier) VERIFIER=$2 ;;
        esac
        shift 2 ;;
      --no-proxy) NO_PROXY=1; shift ;;
      --update) UPDATE=1; shift ;;
      --auto-rollback) AUTO_ROLLBACK=1; shift ;;
      *) die "未知参数：$1" ;;
    esac
  done
  [[ "$AUTO_ROLLBACK" == 0 || "$UPDATE" == 1 ]] || die '--auto-rollback 仅用于 --update'
  check_environment
  umask 077
  # The package's version check executes a binary. /tmp is frequently noexec;
  # stage beside the installation filesystem instead of weakening that mount.
  WORK=$(mktemp -d /opt/ctlvps-install.XXXXXXXX)
  trap cleanup EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM
  info '准备安装依赖'
  export DEBIAN_FRONTEND=noninteractive
  apt-get update
  apt-get install -y --no-install-recommends ca-certificates curl tar coreutils
  if [[ "$VERSION" == latest ]]; then
    [[ -z "$ASSETS_DIR" ]] || die '--assets-dir 必须同时指定确切 --version'
    local resolved
    resolved=$(curl --fail --silent --show-error --location --head --proto '=https' --proto-redir '=https' \
      --retry 3 --connect-timeout 15 --max-time 60 --output /dev/null --write-out '%{url_effective}' "https://github.com/$REPOSITORY/releases/latest")
    [[ "$resolved" == "https://github.com/$REPOSITORY/releases/tag/"* ]] || die '无法解析最新正式版本'
    VERSION=${resolved##*/}
    valid_version "$VERSION" || die '上游版本格式无效'
  fi
  if [[ "$UPDATE" == 1 ]]; then
    local previous_version target_order previous_order
    previous_version=$(cat "$PREVIOUS/VERSION")
    valid_version "$previous_version" || die '现有版本信息无效'
    target_order=$(version_order "$VERSION")
    previous_order=$(version_order "$previous_version")
    if dpkg --compare-versions "$target_order" lt "$previous_order"; then
      die '不支持直接降级；请使用旧版本及其对应的数据备份恢复'
    fi
  fi
  local asset="ctlvps-$VERSION-linux-$ARCH.tar.gz"
  info "下载并验证 $VERSION（$ARCH）"
  if [[ -n "$ASSETS_DIR" ]]; then
    cp -- "$ASSETS_DIR/$asset" "$WORK/$asset"
    cp -- "$ASSETS_DIR/SHA256SUMS" "$WORK/SHA256SUMS"
  else
    local base="https://github.com/$REPOSITORY/releases/download/$VERSION"
    download "$base/$asset" "$WORK/$asset"
    download "$base/SHA256SUMS" "$WORK/SHA256SUMS"
  fi
  local expected
  expected=$(awk -v name="$asset" '$2 == name { print $1 }' "$WORK/SHA256SUMS")
  [[ "$expected" =~ ^[a-fA-F0-9]{64}$ ]] || die '校验清单缺失或重复'
  printf '%s  %s\n' "$expected" "$asset" > "$WORK/selected.sha256"
  (cd "$WORK" && sha256sum --check --status selected.sha256) || die 'SHA256 校验失败，未更换现有程序'
  mkdir "$WORK/package"
  local helper="ctlvps-verify-linux-$ARCH" helper_digest
  if [[ -n "$ASSETS_DIR" ]]; then
    cp -- "$ASSETS_DIR/$helper" "$WORK/backup-tool"
  else
    download "https://github.com/$REPOSITORY/releases/download/$VERSION/$helper" "$WORK/backup-tool"
  fi
  helper_digest=$(awk -v name="$helper" '$2 == name { print $1 }' "$WORK/SHA256SUMS")
  [[ "$helper_digest" =~ ^[a-fA-F0-9]{64}$ ]] || die '恢复工具校验清单无效'
  [[ "$(sha256sum "$WORK/backup-tool" | cut -d ' ' -f 1)" == "$helper_digest" ]] || die '恢复工具 SHA256 校验失败'
  chmod 0700 "$WORK/backup-tool"
  "$WORK/backup-tool" accept-checksum controller "$VERSION" "$expected" "$WORK/$asset" || die '发行包验证失败，原服务保持运行'
  if [[ "$UPDATE" == 1 && ! -e "$PREVIOUS/VERIFIED-SHA256" ]]; then
    # Establish a recovery receipt from the publisher's original old archive,
    # never from the installed directory's self-reported contents.
    local old_asset old_version old_digest
    old_version=$(cat "$PREVIOUS/VERSION")
    valid_version "$old_version" || die '旧版版本信息无效'
    old_asset="ctlvps-$old_version-linux-$ARCH.tar.gz"
    if [[ -n "$ASSETS_DIR" && -f "$ASSETS_DIR/$old_asset" ]]; then
      cp -- "$ASSETS_DIR/$old_asset" "$WORK/$old_asset"
      cp -- "$ASSETS_DIR/SHA256SUMS" "$WORK/old-SHA256SUMS"
    else
      download "https://github.com/$REPOSITORY/releases/download/$old_version/$old_asset" "$WORK/$old_asset"
      download "https://github.com/$REPOSITORY/releases/download/$old_version/SHA256SUMS" "$WORK/old-SHA256SUMS"
    fi
    old_digest=$(awk -v name="$old_asset" '$2 == name { print $1 }' "$WORK/old-SHA256SUMS")
    [[ "$old_digest" =~ ^[a-fA-F0-9]{64}$ ]] || die '旧版恢复包校验清单无效'
    "$WORK/backup-tool" accept-checksum controller "$old_version" "$old_digest" "$WORK/$old_asset" || die '旧版恢复包校验失败，原服务保持运行'
    "$WORK/backup-tool" prepare-recovery controller "$old_digest" "$PREVIOUS" "$WORK/legacy-recovery.json" || die '现有旧版文件与官方恢复包不一致，原服务保持运行'
    printf '%s\n' "$old_digest" > "$PREVIOUS/VERIFIED-SHA256"
  fi
  tar -tzf "$WORK/$asset" > "$WORK/members"
  if grep -Eq '(^/|(^|/)\.\.(/|$))' "$WORK/members"; then die '发行包包含非法路径'; fi
  if ! tar -tvzf "$WORK/$asset" | LC_ALL=C awk 'substr($0,1,1) != "-" && substr($0,1,1) != "d" { exit 1 }'; then
    die '发行包只能包含普通文件和目录'
  fi
  tar --extract --gzip --file "$WORK/$asset" --directory "$WORK/package" --no-same-owner --no-same-permissions
  printf "%s\n" "$expected" > "$WORK/package/VERIFIED-SHA256"
  local file
  for file in ctlvpsd agents/ctlvps-agent-linux-amd64 agents/ctlvps-agent-linux-arm64 ctlvpsd.service VERSION REPOSITORY; do
    [[ -f "$WORK/package/$file" && ! -L "$WORK/package/$file" ]] || die "发行包缺少常规文件：$file"
  done
  [[ "$(cat "$WORK/package/VERSION")" == "$VERSION" && "$(cat "$WORK/package/REPOSITORY")" == "$REPOSITORY" ]] || die '发行包版本或仓库不匹配'
  chmod 0755 "$WORK/package/ctlvpsd"
  "$WORK/package/ctlvpsd" version

  if ! getent passwd ctlvps >/dev/null; then
    useradd --system --user-group --home-dir "$INSTALL_DIR" --no-create-home --shell /usr/sbin/nologin ctlvps
  fi
  getent group ctlvps >/dev/null || die 'ctlvps 用户组不存在'
  [[ "$(id -u ctlvps)" != 0 ]] || die 'ctlvps 服务用户不能是 root'
  install -d -m 0755 "$INSTALL_DIR" "$INSTALL_DIR/releases"
  install -d -m 0700 -o ctlvps -g ctlvps "$INSTALL_DIR/data"
  install -d -m 0750 -o root -g ctlvps /etc/ctlvps
  local release_dir
  release_dir=$(mktemp -d "$INSTALL_DIR/releases/$VERSION.XXXXXXXX")
  NEW_RELEASE=$release_dir
  cp -R "$WORK/package/." "$release_dir/"
  chmod -R a+rX "$release_dir"
  chmod 0755 "$release_dir" "$release_dir/ctlvpsd" "$release_dir"/agents/ctlvps-agent-linux-*

  if [[ "$UPDATE" == 1 ]]; then
    [[ -f "$PREVIOUS/VERIFIED-SHA256" && ! -L "$PREVIOUS/VERIFIED-SHA256" ]] || die '缺少安全恢复记录，原服务保持运行'
    "$WORK/backup-tool" prepare-recovery controller "$(cat "$PREVIOUS/VERIFIED-SHA256")" "$PREVIOUS" "$WORK/recovery.json" || die '恢复点不安全或文件已改变，原服务保持运行'
    reserve_update_space
    install -d -m 0700 "$INSTALL_DIR/backups"
    BACKUP=$(mktemp -d "$INSTALL_DIR/backups/pre-upgrade-$(date -u +%Y%m%dT%H%M%SZ).XXXXXXXX")
    cp -- "$ENV_FILE" "$BACKUP/ctlvpsd.env"
    cp -- "$SERVICE_FILE" "$BACKUP/ctlvpsd.service"
    if [[ -f /etc/systemd/system/ctlvps-maintenance.service ]]; then cp -- /etc/systemd/system/ctlvps-maintenance.service "$BACKUP/ctlvps-maintenance.service"; fi
    printf '%s\n' "$PREVIOUS" > "$BACKUP/previous-release"
    rm -f -- "$RESERVATION"
    RESERVATION=''
    info '停止控制端并备份数据（包含 SQLite WAL 与连接日志）'
    systemctl stop ctlvpsd
    STOPPED=1
    if [[ ! -e /etc/ctlvps/secrets.key ]]; then
      if [[ -f "$INSTALL_DIR/data/ctlvps.db.key" ]]; then
        install -m 0640 -o root -g ctlvps "$INSTALL_DIR/data/ctlvps.db.key" /etc/ctlvps/secrets.key
      else
        head -c 32 /dev/urandom > "$WORK/secrets.key"
        install -m 0640 -o root -g ctlvps "$WORK/secrets.key" /etc/ctlvps/secrets.key
      fi
    fi
    tar --exclude=data/ctlvps.db.key --exclude=data/backups -czf "$WORK/data.tar.gz" -C "$INSTALL_DIR" data
    "$WORK/backup-tool" backup seal /etc/ctlvps/secrets.key "$WORK/data.tar.gz" "$BACKUP/data.tar.gz.enc"
    rm -f "$WORK/data.tar.gz"
    info "备份已保存到 $BACKUP"
  else
    cat > "$WORK/ctlvpsd.env" <<EOF
CTLVPS_SITE_URL=$SITE_URL
CTLVPS_LISTEN=127.0.0.1:8080
CTLVPS_DATA_DIR=/opt/ctlvps/data
CTLVPS_AGENT_BIN_DIR=/opt/ctlvps/agents
CTLVPS_TRUSTED_PROXIES=127.0.0.1/32,::1/128
CTLVPS_SECRETS_KEY_FILE=/etc/ctlvps/secrets.key
CTLVPS_LOG_LEVEL=info
EOF
    install -m 0600 "$WORK/ctlvpsd.env" "$ENV_FILE"
  fi
  if [[ ! -e /etc/ctlvps/secrets.key ]]; then
    if [[ -f "$INSTALL_DIR/data/ctlvps.db.key" && ! -L "$INSTALL_DIR/data/ctlvps.db.key" ]]; then
      install -m 0640 -o root -g ctlvps "$INSTALL_DIR/data/ctlvps.db.key" /etc/ctlvps/secrets.key
    else
      head -c 32 /dev/urandom > "$WORK/secrets.key"
      install -m 0640 -o root -g ctlvps "$WORK/secrets.key" /etc/ctlvps/secrets.key
    fi
  fi
  [[ ! -L /etc/ctlvps/secrets.key ]] || die '数据密钥不得是符号链接'
  if ! grep -q '^CTLVPS_SECRETS_KEY_FILE=' "$ENV_FILE"; then
    printf '\nCTLVPS_SECRETS_KEY_FILE=/etc/ctlvps/secrets.key\n' >> "$ENV_FILE"
  fi
  install -m 0644 "$release_dir/ctlvpsd.service" "$SERVICE_FILE"
  if [[ -f "$release_dir/ctlvps-maintenance.service" ]]; then
    install -m 0644 "$release_dir/ctlvps-maintenance.service" /etc/systemd/system/ctlvps-maintenance.service
  fi
  ln -s "$release_dir" "$release_dir/.activate"
  mv -Tf "$release_dir/.activate" "$INSTALL_DIR/current"
  SWITCHED=1 STOPPED=1
  if [[ "$UPDATE" == 0 ]]; then
    ln -s current/ctlvpsd "$INSTALL_DIR/ctlvpsd"
    ln -s current/agents "$INSTALL_DIR/agents"
  fi
  if [[ -f "$release_dir/uninstall.sh" ]]; then
    ln -sfn current/uninstall.sh "$INSTALL_DIR/uninstall.sh"
  fi
  printf '%s\n' "$REPOSITORY" > "$INSTALL_DIR/REPOSITORY"
  chmod 0644 "$INSTALL_DIR/REPOSITORY"
  systemctl daemon-reload
  systemctl enable --now ctlvpsd
  # --now does not restart an already active service; upgrades were stopped above.
  local ready=0 attempt
  for ((attempt = 0; attempt < 30; attempt++)); do
    if systemctl is-active --quiet ctlvpsd && curl --noproxy '*' --fail --silent --max-time 2 "http://$LISTEN/healthz" > /dev/null; then
      ready=1; break
    fi
    sleep 1
  done
  [[ "$ready" == 1 ]] || die '启动健康检查失败'
  if [[ -f "$release_dir/ctlvps-maintenance.service" ]]; then
    systemctl enable ctlvps-maintenance
    systemctl restart ctlvps-maintenance
  fi
  if [[ "$UPDATE" == 0 && "$NO_PROXY" == 0 ]]; then install_caddy; fi
  if [[ -f "$INSTALL_DIR/data/ctlvps.db.key" && ! -L "$INSTALL_DIR/data/ctlvps.db.key" ]] && cmp -s "$INSTALL_DIR/data/ctlvps.db.key" /etc/ctlvps/secrets.key; then
    rm -f -- "$INSTALL_DIR/data/ctlvps.db.key"
  fi
  if [[ -f "$release_dir/ctlvps-verify" ]]; then
    install -d -m 0755 /usr/local/libexec
    local verifier_stage
    verifier_stage=$(mktemp "$(dirname "$VERIFIER")/.ctlvps-verify.XXXXXXXX")
    install -m 0755 "$release_dir/ctlvps-verify" "$verifier_stage"
    mv -Tf -- "$verifier_stage" "$VERIFIER"
  fi
  for file in install.sh install-agent.sh; do
    if [[ -f "$release_dir/$file" ]]; then
      install -m 0755 "$release_dir/$file" "/usr/local/libexec/ctlvps-$file"
    fi
  done
  COMPLETE=1
  info "控制端 $VERSION 已启动：$SITE_URL"
  if [[ -f "$INSTALL_DIR/uninstall.sh" ]]; then
    info '卸载预览：sudo bash /opt/ctlvps/uninstall.sh --controller --dry-run'
  fi
  if [[ -f "$INSTALL_DIR/data/setup-token" ]]; then
    info '首次创建管理员需要初始化令牌，请在服务器终端执行：'
    printf '  sudo cat /opt/ctlvps/data/setup-token\n'
  fi
  printf '日志：journalctl -u ctlvpsd -f\n'
  if [[ "$UPDATE" == 0 && "$NO_PROXY" == 0 ]]; then
    info 'Caddy 已配置；证书签发依赖 DNS 和公网端口，请从浏览器验证 HTTPS'
  fi
}

if [[ "${BASH_SOURCE[0]:-$0}" == "$0" ]]; then main "$@"; fi
