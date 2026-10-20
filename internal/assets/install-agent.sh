#!/usr/bin/env bash
# ctlvps-agent installer. Usage:
#   curl -fsSL https://github.com/YongshengWin/VpsCT/releases/latest/download/install-agent.sh | sudo bash -s -- --server https://panel.example.com --token <enroll-token>
set -euo pipefail

SERVER=""
TOKEN=""
BIN_DIR="/usr/local/bin"
CONF_DIR="/etc/ctlvps"
STATE_DIR="/var/lib/ctlvps-agent"
UPDATE=0
umask 077

while [[ $# -gt 0 ]]; do
  case "$1" in
    --server) SERVER="$2"; shift 2 ;;
    --token) TOKEN="$2"; shift 2 ;;
    --bin-dir) BIN_DIR="$2"; shift 2 ;;
    --update) UPDATE=1; shift ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

if [[ "$SERVER" != https://* || "$SERVER" == *[[:space:]]* ]]; then
 echo "HTTPS controller URL required" >&2; exit 2
fi
if [[ "$BIN_DIR" != /usr/local/bin ]]; then echo "binary directory must be /usr/local/bin" >&2; exit 2; fi
if [[ -z "$SERVER" ]]; then
  echo "usage: install-agent.sh --server <url> --token <enroll-token>" >&2
  echo "       install-agent.sh --update --server <url>" >&2
  exit 2
fi
if [[ "$UPDATE" -eq 0 && -z "$TOKEN" ]]; then
  echo "usage: install-agent.sh --server <url> --token <enroll-token>" >&2
  exit 2
fi
if [[ "$(id -u)" -ne 0 ]]; then
  echo "must run as root" >&2
  exit 1
fi
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) echo "ARM 支持已暂停；当前仅支持 Linux AMD64" >&2; exit 1 ;;
  *) echo "unsupported architecture: $ARCH" >&2; exit 1 ;;
esac

INIT=''
if [[ -d /run/systemd/system ]] && command -v systemctl >/dev/null 2>&1; then
  INIT=systemd
elif [[ -f /run/openrc/softlevel ]] && command -v rc-service >/dev/null 2>&1 && command -v rc-update >/dev/null 2>&1; then
  INIT=openrc
else
  echo "a running systemd or OpenRC host is required" >&2
  exit 1
fi
# Alpine's default BusyBox tools do not provide all installer/deletion guards.
# Updates need the same tools and runtime sandbox as initial installations.
if [[ "$INIT" == openrc ]]; then
  command -v apk >/dev/null 2>&1 || { echo "OpenRC support currently requires Alpine Linux" >&2; exit 1; }
  apk add --no-cache bash curl ca-certificates nftables unzip tar chrony coreutils findmnt flock setpriv bubblewrap shadow >/dev/null
fi
if ! command -v flock >/dev/null 2>&1; then
  echo "flock is required; install util-linux first" >&2
  exit 1
fi
install -d -m 0755 /run/lock
exec 9>/run/lock/ctlvps-install.lock
flock -n 9 || { echo "another installation or maintenance task is running" >&2; exit 1; }



# Download a complete release from the fixed publisher. The controller supplies
# enrollment/configuration only; it cannot choose executable bytes or checksums.
RELEASE_VERSION='__VERSION__'
if [[ "$RELEASE_VERSION" == '__VERSION__' ]]; then
  RELEASE_VERSION=$(curl -fLsS --proto '=https' --proto-redir '=https' --max-time 60 https://api.github.com/repos/YongshengWin/VpsCT/releases/latest | sed -n 's/.*"tag_name": *"\([^" ]*\)".*/\1/p')
fi
[[ "$RELEASE_VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9][A-Za-z0-9.-]*)?$ ]] || { echo 'invalid release version' >&2; exit 1; }
WORK=$(mktemp -d)
agent_stage=''
# OpenRC daemons may inherit descriptor 9. Explicitly release our flock on
# exit so a successful install cannot permanently block subsequent updates.
trap 'flock -u 9; rm -rf -- "$WORK"; if [[ -n "$agent_stage" ]]; then rm -f -- "$agent_stage"; fi' EXIT
RELEASE_BASE="https://github.com/YongshengWin/VpsCT/releases/download/$RELEASE_VERSION"
agent_restart() {
  if [[ "$INIT" == openrc ]]; then rc-service ctlvps-agent restart; else systemctl restart ctlvps-agent; fi
}
agent_running() {
  if [[ "$INIT" == openrc ]]; then rc-service ctlvps-agent status >/dev/null; else systemctl is-active --quiet ctlvps-agent; fi
}
wait_openrc_ready() {
  local attempt deadline=$((SECONDS + 90))
  # A supervisor PID alone does not prove the agent refreshed its helpers.
  # The fixed read-only CLI binds readiness to the current process/executable.
  for ((attempt = 0; attempt < 90 && SECONDS < deadline; attempt++)); do
    if agent_running && "$BIN_DIR/ctlvps-agent" openrc-ready >/dev/null 2>&1; then return 0; fi
    sleep 1 || return 1
  done
  echo 'OpenRC agent did not become ready within the startup window' >&2
  return 1
}
restart_updated_agent() {
  local started=0
  if [[ "$INIT" == openrc ]]; then
    if agent_restart; then started=1; fi
    # Run must take this lock to refresh helpers. Never wait while holding it.
    flock -u 8 || return 1
    [[ "$started" == 1 ]] && wait_openrc_ready
  else
    agent_restart && sleep 2 && agent_running
  fi
}
agent_status() {
  if [[ "$INIT" == openrc ]]; then rc-service ctlvps-agent status; else systemctl --no-pager --lines=5 status ctlvps-agent; fi
}
agent_logs() {
  if [[ "$INIT" == openrc ]]; then echo 'tail -f /var/log/messages'; else echo 'journalctl -u ctlvps-agent -f'; fi
}
uninstall_preview() {
  if [[ "$INIT" == openrc ]]; then
    echo '==> uninstall preview (root): bash /usr/local/libexec/ctlvps-agent-uninstall.sh --agent --dry-run'
  else
    echo '==> uninstall preview: sudo bash /usr/local/libexec/ctlvps-agent-uninstall.sh --agent --dry-run'
  fi
}
prepare_openrc_agent() {
  local candidate=$1
  "$candidate" openrc-check
  if [[ -e /etc/init.d/ctlvps-agent || -L /etc/init.d/ctlvps-agent ]]; then
    "$BIN_DIR/ctlvps-agent" openrc-verify ctlvps-agent.service
  fi
  "$candidate" openrc-script ctlvps-agent.service > "$WORK/agent.init.new"
  [[ -s "$WORK/agent.init.new" ]] || { echo 'empty OpenRC service script' >&2; exit 1; }
  sh -n "$WORK/agent.init.new"
}
install_openrc_agent() {
  local staged
  staged=$(mktemp /etc/init.d/.ctlvps-agent.XXXXXXXX)
  install -m 0755 "$WORK/agent.init.new" "$staged"
  mv -Tf -- "$staged" /etc/init.d/ctlvps-agent
}
download_agent() {
  local asset="ctlvps-agent-linux-$ARCH" helper="ctlvps-verify-linux-$ARCH" expected helper_stage
  TMP="$WORK/$asset"
  curl -fLsS --proto '=https' --proto-redir '=https' --max-time 300 --max-filesize 134217728 "$RELEASE_BASE/$asset" -o "$TMP"
  curl -fLsS --proto '=https' --proto-redir '=https' --max-time 60 --max-filesize 1048576 "$RELEASE_BASE/SHA256SUMS" -o "$WORK/SHA256SUMS"
  expected=$(awk -v name="$asset" '$2 == name { print $1 }' "$WORK/SHA256SUMS")
  [[ "$expected" =~ ^[a-fA-F0-9]{64}$ && "$(sha256sum "$TMP" | cut -d ' ' -f 1)" == "$expected" ]] || { echo 'agent SHA256 mismatch' >&2; exit 1; }
  curl -fLsS --proto '=https' --proto-redir '=https' --max-time 300 --max-filesize 134217728 "$RELEASE_BASE/$helper" -o "$WORK/$helper"
  expected=$(awk -v name="$helper" '$2 == name { print $1 }' "$WORK/SHA256SUMS")
  [[ "$expected" =~ ^[a-fA-F0-9]{64}$ && "$(sha256sum "$WORK/$helper" | cut -d ' ' -f 1)" == "$expected" ]] || { echo 'recovery helper SHA256 mismatch' >&2; exit 1; }
  curl -fLsS --proto '=https' --proto-redir '=https' --max-time 60 --max-filesize 1048576 "$RELEASE_BASE/uninstall.sh" -o "$WORK/uninstall.sh"
  expected=$(awk '$2 == "uninstall.sh" { print $1 }' "$WORK/SHA256SUMS")
  [[ "$expected" =~ ^[a-fA-F0-9]{64}$ && "$(sha256sum "$WORK/uninstall.sh" | cut -d ' ' -f 1)" == "$expected" ]] || { echo 'uninstaller SHA256 mismatch' >&2; exit 1; }
  install -d -m 0755 /usr/local/libexec
  helper_stage=$(mktemp /usr/local/libexec/.ctlvps-verify.XXXXXXXX)
  install -m 0755 "$WORK/$helper" "$helper_stage"
  mv -Tf -- "$helper_stage" /usr/local/libexec/ctlvps-verify
  helper_stage=$(mktemp /usr/local/libexec/.ctlvps-agent-uninstall.XXXXXXXX)
  install -m 0755 "$WORK/uninstall.sh" "$helper_stage"
  mv -Tf -- "$helper_stage" /usr/local/libexec/ctlvps-agent-uninstall.sh

}

if [[ "$UPDATE" -eq 1 ]]; then
  if [[ ! -f "$STATE_DIR/state.json" ]]; then
    echo "no existing agent state in $STATE_DIR; use a full enroll instead" >&2
    exit 1
  fi
  echo "==> updating ctlvps-agent (linux-$ARCH)"
  download_agent
	# Serialize with the agent and web-maintenance configuration target.
	install -d -m 0700 /var/lib/ctlvps-maintenance
	exec 8>/var/lib/ctlvps-maintenance/agent-configuration.lock
	flock -n 8 || { echo 'agent configuration or maintenance is running; retry after it finishes' >&2; exit 1; }
  [[ -f "$BIN_DIR/ctlvps-agent" && ! -L "$BIN_DIR/ctlvps-agent" ]] || { echo 'existing agent must be a regular file' >&2; exit 1; }
  cp -- "$BIN_DIR/ctlvps-agent" "$WORK/agent.previous"
  agent_stage=$(mktemp "$BIN_DIR/.ctlvps-agent.XXXXXXXX")
  install -m 0755 "$TMP" "$agent_stage"
  if ! "$agent_stage" version; then rm -f -- "$agent_stage"; exit 1; fi
	if "$BIN_DIR/ctlvps-agent" capabilities >/dev/null 2>&1; then
	  if ! "$BIN_DIR/ctlvps-agent" check-update --candidate "$agent_stage" --state "$STATE_DIR"; then
	    rm -f -- "$agent_stage"; exit 1
	  fi
	fi
  if [[ "$INIT" == openrc ]]; then
    prepare_openrc_agent "$agent_stage"
    cp -- /etc/init.d/ctlvps-agent "$WORK/agent.init.previous"
    rc-service ctlvps-agent stop
    "$BIN_DIR/ctlvps-agent" openrc-stop ctlvps-agent.service
  fi
  mv -Tf -- "$agent_stage" "$BIN_DIR/ctlvps-agent"
  if [[ "$INIT" == openrc ]]; then install_openrc_agent; fi
  if ! restart_updated_agent; then
    if [[ "$INIT" == openrc ]]; then
      flock -w 30 8 || { echo 'agent update failed; cannot acquire the configuration lock for rollback, installed candidate left unchanged' >&2; exit 1; }
      rc-service ctlvps-agent stop || true
      "$BIN_DIR/ctlvps-agent" openrc-stop ctlvps-agent.service || { echo 'agent update failed; cannot stop the candidate cgroup for rollback, installed candidate left unchanged' >&2; exit 1; }
      if agent_running; then
        echo 'agent update failed; candidate supervisor is still running, installed candidate left unchanged' >&2
        exit 1
      fi
    fi
    agent_stage=$(mktemp "$BIN_DIR/.ctlvps-agent.XXXXXXXX")
    install -m 0755 "$WORK/agent.previous" "$agent_stage"
    mv -Tf -- "$agent_stage" "$BIN_DIR/ctlvps-agent"
    if [[ "$INIT" == openrc ]]; then
      cp -- "$WORK/agent.init.previous" "$WORK/agent.init.new"
      install_openrc_agent
    fi
    if [[ "$INIT" == openrc ]]; then
      if ! restart_updated_agent; then
        echo 'agent update failed; previous binary restored but did not become ready; check service logs' >&2
        exit 1
      fi
    else
      agent_restart
    fi
    echo 'agent update failed; previous binary restored' >&2
    exit 1
  fi
  agent_status
  uninstall_preview
  echo "==> agent updated. logs: $(agent_logs)"
  exit 0
fi

echo "==> installing dependencies"
if command -v apt-get >/dev/null 2>&1; then
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq >/dev/null
  apt-get install -y -qq nftables curl ca-certificates unzip tar chrony >/dev/null
elif command -v dnf >/dev/null 2>&1; then
  dnf install -y -q nftables curl ca-certificates unzip tar chrony >/dev/null
elif command -v yum >/dev/null 2>&1; then
  yum install -y -q nftables curl ca-certificates unzip tar chrony >/dev/null
elif [[ "$INIT" != openrc ]]; then
  echo 'no supported package manager' >&2; exit 1
fi
if [[ "$INIT" == openrc ]]; then
  # The agent reconciles its own tables. Do not start the packaged nftables
  # service here: its configuration may flush an operator's existing ruleset.
  rc-update add chronyd default
  rc-service chronyd start
else
  systemctl enable --now nftables >/dev/null 2>&1 || true
  systemctl enable --now chrony >/dev/null 2>&1 || systemctl enable --now chronyd >/dev/null 2>&1 || true
fi

echo "==> downloading ctlvps-agent (linux-$ARCH)"
install -d -m 0755 "$BIN_DIR"
download_agent
# /tmp may be mounted noexec. Execute the checked candidate only from the
# destination filesystem, then atomically promote it after OpenRC preflight.
agent_stage=$(mktemp "$BIN_DIR/.ctlvps-agent.XXXXXXXX")
install -m 0755 "$TMP" "$agent_stage"
if [[ "$INIT" == openrc ]]; then prepare_openrc_agent "$agent_stage"; fi
mv -Tf -- "$agent_stage" "$BIN_DIR/ctlvps-agent"
rm -f "$TMP"

mkdir -p "$CONF_DIR" "$STATE_DIR"
chmod 0700 "$STATE_DIR"

echo "==> enrolling with $SERVER"
"$BIN_DIR/ctlvps-agent" enroll --server "$SERVER" --token "$TOKEN" --state "$STATE_DIR"

if [[ "$INIT" == openrc ]]; then
  install_openrc_agent
  "$BIN_DIR/ctlvps-agent" openrc-sync
  rc-update add ctlvps-agent default
  rc-service ctlvps-agent start
else
cat > /etc/systemd/system/ctlvps-agent.service <<EOF
[Unit]
Description=ctlvps agent
After=network-online.target nftables.service
Wants=network-online.target

[Service]
Type=simple
ExecStart=$BIN_DIR/ctlvps-agent run --state $STATE_DIR
Restart=always
RestartSec=3
LimitNOFILE=1048576
Environment=GOMEMLIMIT=64MiB
MemoryAccounting=yes
MemoryHigh=160M
MemoryMax=192M
TasksMax=128
Nice=-5
NoNewPrivileges=true
ProtectHome=true
PrivateTmp=true
RestrictSUIDSGID=true

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now ctlvps-agent
fi
if [[ "$INIT" == openrc ]]; then
  wait_openrc_ready || { echo 'agent installation did not become ready; check service logs before retrying' >&2; exit 1; }
else
  sleep 2
fi
agent_status
echo "==> done. logs: $(agent_logs)"

uninstall_preview
