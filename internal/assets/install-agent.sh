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
if ! command -v systemctl >/dev/null 2>&1; then
  echo "systemd is required" >&2
  exit 1
fi
if ! command -v flock >/dev/null 2>&1; then
  echo "flock is required; install util-linux first" >&2
  exit 1
fi
exec 9>/run/lock/ctlvps-install.lock
flock -n 9 || { echo "another installation or maintenance task is running" >&2; exit 1; }

ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) echo "unsupported architecture: $ARCH" >&2; exit 1 ;;
esac

# Download a complete release from the fixed publisher. The controller supplies
# enrollment/configuration only; it cannot choose executable bytes or checksums.
RELEASE_VERSION='__VERSION__'
if [[ "$RELEASE_VERSION" == '__VERSION__' ]]; then
  RELEASE_VERSION=$(curl -fLsS --proto '=https' --proto-redir '=https' --max-time 60 https://api.github.com/repos/YongshengWin/VpsCT/releases/latest | sed -n 's/.*"tag_name": *"\([^" ]*\)".*/\1/p')
fi
[[ "$RELEASE_VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9][A-Za-z0-9.-]*)?$ ]] || { echo 'invalid release version' >&2; exit 1; }
WORK=$(mktemp -d)
trap 'rm -rf -- "$WORK"' EXIT
RELEASE_BASE="https://github.com/YongshengWin/VpsCT/releases/download/$RELEASE_VERSION"
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
  [[ -f "$BIN_DIR/ctlvps-agent" && ! -L "$BIN_DIR/ctlvps-agent" ]] || { echo 'existing agent must be a regular file' >&2; exit 1; }
  cp -- "$BIN_DIR/ctlvps-agent" "$WORK/agent.previous"
  agent_stage=$(mktemp "$BIN_DIR/.ctlvps-agent.XXXXXXXX")
  install -m 0755 "$TMP" "$agent_stage"
  if ! "$agent_stage" version; then rm -f -- "$agent_stage"; exit 1; fi
  mv -Tf -- "$agent_stage" "$BIN_DIR/ctlvps-agent"
  if ! systemctl restart ctlvps-agent || ! sleep 2 || ! systemctl is-active --quiet ctlvps-agent; then
    agent_stage=$(mktemp "$BIN_DIR/.ctlvps-agent.XXXXXXXX")
    install -m 0755 "$WORK/agent.previous" "$agent_stage"
    mv -Tf -- "$agent_stage" "$BIN_DIR/ctlvps-agent"
    systemctl restart ctlvps-agent
    echo 'agent update failed; previous binary restored' >&2
    exit 1
  fi
  systemctl --no-pager --lines=5 status ctlvps-agent
  echo "==> uninstall preview: sudo bash /usr/local/libexec/ctlvps-agent-uninstall.sh --agent --dry-run"
  echo "==> agent updated. logs: journalctl -u ctlvps-agent -f"
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
elif command -v apk >/dev/null 2>&1; then
  apk add --no-cache nftables curl ca-certificates unzip tar chrony >/dev/null
fi
systemctl enable --now nftables >/dev/null 2>&1 || true
systemctl enable --now chrony >/dev/null 2>&1 || systemctl enable --now chronyd >/dev/null 2>&1 || true

echo "==> downloading ctlvps-agent (linux-$ARCH)"
download_agent
install -m 0755 "$TMP" "$BIN_DIR/ctlvps-agent"
rm -f "$TMP"

mkdir -p "$CONF_DIR" "$STATE_DIR"
chmod 0700 "$STATE_DIR"

echo "==> enrolling with $SERVER"
"$BIN_DIR/ctlvps-agent" enroll --server "$SERVER" --token "$TOKEN" --state "$STATE_DIR"

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
sleep 2
systemctl --no-pager --lines=5 status ctlvps-agent || true
echo "==> done. logs: journalctl -u ctlvps-agent -f"

echo "==> uninstall preview: sudo bash /usr/local/libexec/ctlvps-agent-uninstall.sh --agent --dry-run"
