#!/usr/bin/env bash
# ctlvps-agent installer. Usage:
#   curl -fsSL https://panel.example.com/install-agent.sh | sudo bash -s -- --server https://panel.example.com --token <enroll-token>
set -euo pipefail

SERVER=""
TOKEN=""
BIN_DIR="/usr/local/bin"
CONF_DIR="/etc/ctlvps"
STATE_DIR="/var/lib/ctlvps-agent"
UPDATE=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --server) SERVER="$2"; shift 2 ;;
    --token) TOKEN="$2"; shift 2 ;;
    --bin-dir) BIN_DIR="$2"; shift 2 ;;
    --update) UPDATE=1; shift ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

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

if [[ "$UPDATE" -eq 1 ]]; then
  if [[ ! -f "$STATE_DIR/state.json" ]]; then
    echo "no existing agent state in $STATE_DIR; use a full enroll instead" >&2
    exit 1
  fi
  echo "==> updating ctlvps-agent (linux-$ARCH)"
  TMP="$(mktemp)"
  curl -fsSL "$SERVER/dl/agent/linux-$ARCH" -o "$TMP"
  install -m 0755 "$TMP" "$BIN_DIR/ctlvps-agent"
  rm -f "$TMP"
  systemctl restart ctlvps-agent
  sleep 2
  systemctl --no-pager --lines=5 status ctlvps-agent || true
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
TMP="$(mktemp)"
curl -fsSL "$SERVER/dl/agent/linux-$ARCH" -o "$TMP"
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
Environment=GOMEMLIMIT=96MiB
MemoryMax=192M
Nice=-5

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now ctlvps-agent
sleep 2
systemctl --no-pager --lines=5 status ctlvps-agent || true
echo "==> done. logs: journalctl -u ctlvps-agent -f"
