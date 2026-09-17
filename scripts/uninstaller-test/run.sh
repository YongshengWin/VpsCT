#!/usr/bin/env bash
# Destructive fixtures: run ONLY in the disposable container created by the runner.
set -euo pipefail
[[ "$(cat /proc/1/comm)" == systemd && -f /.dockerenv ]] || exit 1
cd /src
units=(ctlvps-agent.service ctlvpsd.service ctlvps-singbox@21001.service ctlvps-snell@21002.service caddy.service)
checks=0
assert() { "$@" || { printf 'Assertion failed: %s\n' "$*" >&2; exit 1; }; }
done_case() { checks=$((checks+1)); printf 'PASS %s\n' "$*"; }
uninstall() { bash /src/uninstall.sh "$@" > /tmp/uninstall-test-output 2>&1 || { cat /tmp/uninstall-test-output; return 1; }; }
reject() { if bash /src/uninstall.sh "$@" > /tmp/uninstall-test-output 2>&1; then cat /tmp/uninstall-test-output; exit 1; fi; }

fixture() {
  local unit executable
  for unit in "${units[@]}"; do systemctl stop "$unit" >/dev/null 2>&1 || true; done
  rm -rf /opt/ctlvps /etc/ctlvps /var/lib/ctlvps-agent /var/log/ctlvps /etc/caddy /var/lib/caddy /etc/systemd/system/ctlvps*.service /etc/systemd/system/caddy.service
  rm -f /etc/systemd/system/multi-user.target.wants/ctlvps* /etc/systemd/system/multi-user.target.wants/caddy.service
  mkdir -p /opt/ctlvps/releases/test/agents /opt/ctlvps/data/backups /opt/ctlvps/backups \
    /opt/ctlvps/bin /etc/ctlvps/sing-box /etc/ctlvps/snell /var/lib/ctlvps-agent/certs /var/log/ctlvps /etc/caddy
  for executable in /opt/ctlvps/releases/test/ctlvpsd /usr/local/bin/ctlvps-agent /opt/ctlvps/bin/sing-box /opt/ctlvps/bin/snell-server /usr/bin/caddy; do
    printf '#!/bin/sh\nexec /bin/sleep infinity\n' > "$executable"
    chmod 0755 "$executable"
  done
  mkdir -p /usr/local/libexec
  cp /src/uninstall.sh /usr/local/libexec/ctlvps-agent-uninstall.sh
  cp /src/uninstall.sh /opt/ctlvps/releases/test/uninstall.sh
  ln -s releases/test /opt/ctlvps/current
  ln -s current/ctlvpsd /opt/ctlvps/ctlvpsd
  ln -s current/agents /opt/ctlvps/agents
  ln -s current/uninstall.sh /opt/ctlvps/uninstall.sh
  touch /opt/ctlvps/REPOSITORY /opt/ctlvps/data/ctlvps.db /opt/ctlvps/data/backups/db \
    /opt/ctlvps/backups/upgrade.tar.gz /var/lib/ctlvps-agent/state.json /var/lib/ctlvps-agent/certs/test.key \
    /var/log/ctlvps/sing-box.log /etc/ctlvps/sing-box/21001.json /etc/ctlvps/snell/21002.conf
  printf 'CTLVPS_DATA_DIR=/opt/ctlvps/data\n' > /etc/ctlvps/ctlvpsd.env
  cat > /etc/caddy/Caddyfile <<'EOF'
# Managed by VpsCT installer
panel.example.test {
    reverse_proxy 127.0.0.1:8080
}
EOF
  mkdir -p /var/lib/caddy/.local/share/caddy/certificates/test-ca/{panel.example.test,other.example.test}
  touch /var/lib/caddy/.local/share/caddy/certificates/test-ca/{panel.example.test,other.example.test}/certificate.key
  python3 - <<'PY'
from pathlib import Path
commands = {
 'ctlvpsd': '/opt/ctlvps/ctlvpsd',
 'ctlvps-agent': '/usr/local/bin/ctlvps-agent run --state /var/lib/ctlvps-agent',
 'ctlvps-singbox@': '/opt/ctlvps/bin/sing-box run -c /etc/ctlvps/sing-box/%i.json',
 'ctlvps-snell@': '/opt/ctlvps/bin/snell-server -c /etc/ctlvps/snell/%i.conf',
 'caddy': '/usr/bin/caddy run --environ --config /etc/caddy/Caddyfile',
}
for name,command in commands.items():
 Path('/etc/systemd/system', name+'.service').write_text(
  '[Unit]\nDescription=Disposable uninstall fixture\n[Service]\nExecStart='+command+
  '\nRestart=always\n[Install]\nWantedBy=multi-user.target\n')
PY
  systemctl daemon-reload
  systemctl reset-failed
  systemctl enable --now "${units[@]}" >/dev/null 2>&1
  nft delete table inet ctlvps 2>/dev/null || true
  nft delete table inet keep_fixture 2>/dev/null || true
  nft add table inet ctlvps
  nft add table inet keep_fixture
  nft delete table inet filter 2>/dev/null || true
  nft -f - <<'NFT'
add table inet filter
add chain inet filter input { type filter hook input priority 0; policy accept; }
add rule inet filter input tcp dport 22 accept comment "keep-ssh"
add rule inet filter input tcp dport 23456 accept comment "ctlvps-node-ingress:tcp:test"
NFT
}

fixture
uninstall --all --purge --remove-caddy --dry-run
for unit in "${units[@]}"; do assert systemctl is-active --quiet "$unit"; done
assert test -f /opt/ctlvps/data/ctlvps.db
assert test -f /var/lib/ctlvps-agent/state.json
done_case 'dry run leaves services and data intact'

reject --all --purge
assert test -f /opt/ctlvps/data/ctlvps.db
assert systemctl is-active --quiet ctlvps-agent
done_case 'noninteractive run requires explicit confirmation'

uninstall --controller --yes
assert test -f /opt/ctlvps/data/ctlvps.db
assert test -f /etc/ctlvps/ctlvpsd.env
assert test ! -e /opt/ctlvps/current
assert test ! -e /opt/ctlvps/uninstall.sh
assert systemctl is-active --quiet ctlvps-agent
assert systemctl is-active --quiet ctlvps-singbox@21001
assert systemctl is-active --quiet caddy
uninstall --controller --purge --yes
assert test ! -e /opt/ctlvps/data
assert test -f /var/lib/ctlvps-agent/state.json
assert nft list table inet ctlvps
assert test -f /usr/local/libexec/ctlvps-agent-uninstall.sh
done_case 'controller uninstall, later purge, and agent coexistence'
bash /usr/local/libexec/ctlvps-agent-uninstall.sh --agent --purge --dry-run > /tmp/standalone-preview
assert systemctl is-active --quiet ctlvps-agent
bash /usr/local/libexec/ctlvps-agent-uninstall.sh --agent --purge --yes > /tmp/standalone-cleanup
assert test ! -e /usr/local/bin/ctlvps-agent
assert test ! -e /var/lib/ctlvps-agent
assert test ! -e /usr/local/libexec/ctlvps-agent-uninstall.sh
done_case 'standalone agent cleanup without controller or panel access'

fixture
uninstall --agent --yes
assert test -f /var/lib/ctlvps-agent/state.json
assert test -f /etc/ctlvps/sing-box/21001.json
assert test ! -e /usr/local/bin/ctlvps-agent
assert test ! -e /opt/ctlvps/bin/sing-box
assert systemctl is-active --quiet ctlvpsd
if systemctl is-active --quiet ctlvps-singbox@21001; then exit 1; fi
if nft list table inet ctlvps 2>/dev/null; then exit 1; fi
assert nft list table inet keep_fixture
assert test -f /usr/local/libexec/ctlvps-agent-uninstall.sh
bash /usr/local/libexec/ctlvps-agent-uninstall.sh --agent --purge --yes > /tmp/standalone-uninstall-output
assert test ! -e /usr/local/libexec/ctlvps-agent-uninstall.sh
assert test ! -e /var/lib/ctlvps-agent
assert test ! -e /etc/ctlvps/sing-box
assert test -f /opt/ctlvps/data/ctlvps.db
assert bash -c 'nft list chain inet filter input | grep -q keep-ssh'
if nft list chain inet filter input | grep -q ctlvps-node-ingress; then exit 1; fi
done_case 'agent stops both cores, preserves controller and unrelated nft table and ingress rules'

fixture
uninstall --all --purge --remove-caddy --yes
assert test ! -e /opt/ctlvps
assert test ! -e /etc/ctlvps
assert test ! -e /var/lib/ctlvps-agent
assert test ! -e /var/log/ctlvps
assert test ! -e /etc/caddy/Caddyfile
assert test ! -e /var/lib/caddy/.local/share/caddy/certificates/test-ca/panel.example.test
assert test -f /var/lib/caddy/.local/share/caddy/certificates/test-ca/other.example.test/certificate.key
assert nft list table inet keep_fixture
uninstall --all --purge --remove-caddy --yes
done_case 'all purge removes owned data and is repeatable'

fixture
sed -i 's/panel.example.test/PANEL.EXAMPLE.TEST/' /etc/caddy/Caddyfile
uninstall --controller --purge --remove-caddy --yes
assert test ! -e /var/lib/caddy/.local/share/caddy/certificates/test-ca/panel.example.test
done_case 'certificate cleanup handles case-insensitive domain names'

fixture
printf '\nother.example.test {\n respond "keep"\n}\n' >> /etc/caddy/Caddyfile
reject --all --purge --remove-caddy --yes
assert systemctl is-active --quiet ctlvpsd
assert test -f /opt/ctlvps/data/ctlvps.db
done_case 'shared Caddy rejected before stopping services'

fixture
sed -i 's|--config /etc/caddy/Caddyfile|--config /important/caddy.json|' /etc/systemd/system/caddy.service
systemctl daemon-reload
reject --controller --purge --remove-caddy --yes
assert systemctl is-active --quiet caddy
assert systemctl is-active --quiet ctlvpsd
done_case 'custom Caddy startup rejected before stopping services'

fixture
mkdir -p /important
touch /important/keep
mv /opt/ctlvps/data /important/data
ln -s /important/data /opt/ctlvps/data
uninstall --controller --purge --yes
assert test -f /important/data/ctlvps.db
done_case 'leaf symlink unlinked without deleting its destination'

fixture
mv /etc/ctlvps /etc/ctlvps-real
ln -s /etc/ctlvps-real /etc/ctlvps
reject --all --purge --yes
assert systemctl is-active --quiet ctlvps-agent
assert test -f /etc/ctlvps-real/sing-box/21001.json
rm /etc/ctlvps
mv /etc/ctlvps-real /etc/ctlvps
done_case 'symlinked parent rejected before stopping services'

fixture
mkdir /opt/ctlvps/data/mounted
mount --bind /important /opt/ctlvps/data/mounted
reject --controller --purge --yes
assert systemctl is-active --quiet ctlvpsd
umount /opt/ctlvps/data/mounted
done_case 'nested bind mount rejected before deletion'

fixture
sed -i 's|CTLVPS_DATA_DIR=.*|CTLVPS_DATA_DIR=/important/data|' /etc/ctlvps/ctlvpsd.env
reject --controller --purge --yes
assert systemctl is-active --quiet ctlvpsd
done_case 'custom controller data path rejected'

fixture
mkdir -p /etc/systemd/system/ctlvps-agent.service.d
printf '[Service]\nEnvironment=TEST=1\n' > /etc/systemd/system/ctlvps-agent.service.d/override.conf
systemctl daemon-reload
reject --agent --purge --yes
assert systemctl is-active --quiet ctlvps-agent
rm -r /etc/systemd/system/ctlvps-agent.service.d
systemctl daemon-reload
done_case 'systemd overrides rejected'

fixture
sed -i 's|--state /var/lib/ctlvps-agent|--state /important|' /etc/systemd/system/ctlvps-agent.service
systemctl daemon-reload
reject --agent --purge --yes
assert test -f /var/lib/ctlvps-agent/state.json
done_case 'custom agent state rejected'

fixture
sed -i '/Restart=always/a ExecStop=/bin/false' /etc/systemd/system/ctlvps-agent.service
systemctl daemon-reload
reject --all --purge --yes
assert test -f /opt/ctlvps/data/ctlvps.db
assert test -f /var/lib/ctlvps-agent/state.json
done_case 'stop failure aborts before deleting files'



fixture
cat > /etc/systemd/system/ctlvps-proxy.slice <<'EOF'
[Slice]
MemoryMax=256M
EOF
cat > /etc/systemd/system/ctlvps-proxy-n2.slice <<'EOF'
[Slice]
IPAccounting=yes
EOF
mkdir -p /etc/systemd/system/ctlvps-snell@21002.service.d
cat > /etc/systemd/system/ctlvps-snell@21002.service.d/meter.conf <<'EOF'
[Service]
Slice=ctlvps-proxy-n2.slice
EOF
nft add table inet ctlvps_nodes
systemctl daemon-reload
systemctl restart ctlvps-snell@21002.service
uninstall --agent --yes
assert test ! -f /etc/systemd/system/ctlvps-proxy-n2.slice
assert test ! -f /etc/systemd/system/ctlvps-snell@21002.service.d/meter.conf
if nft list table inet ctlvps_nodes 2>/dev/null; then exit 1; fi
assert nft list table inet keep_fixture
done_case 'shared process meters and generated Snell slice drop-in are removed safely'

fixture
mkdir -p /etc/ctlvps-proxy/public /etc/ctlvps-proxy/private /var/lib/ctlvps-proxy/public/acme
printf 'synthetic' > /etc/ctlvps-proxy/public/config.json
python3 - <<'PYTEST'
from pathlib import Path
for name,command in {
 'ctlvps-proxy-guard':'/usr/local/bin/ctlvps-agent proxy-guard',
 'ctlvps-singbox':'/usr/local/bin/ctlvps-agent proxy-exec singbox run public',
 'ctlvps-singbox-private':'/usr/local/bin/ctlvps-agent proxy-exec singbox run private',
 'ctlvps-snell@21002':'/usr/local/bin/ctlvps-agent proxy-exec snell run 21002',
}.items():
 Path('/etc/systemd/system/'+name+'.service').write_text('[Unit]\nDescription=Isolated proxy uninstall fixture\n[Service]\nExecStart='+command+'\n[Install]\nWantedBy=multi-user.target\n')
for profile in ('public','private'):
 Path('/etc/systemd/system/ctlvps-proxy-'+profile+'.slice').write_text('[Slice]\n')
Path('/etc/systemd/system/ctlvps-proxy-guard.timer').write_text('[Timer]\nOnBootSec=1h\nUnit=ctlvps-proxy-guard.service\n[Install]\nWantedBy=timers.target\n')
PYTEST
systemctl daemon-reload
systemctl enable --now ctlvps-singbox.service ctlvps-singbox-private.service >/dev/null 2>&1
systemctl enable --now ctlvps-proxy-guard.timer >/dev/null 2>&1
systemctl restart ctlvps-snell@21002.service
uninstall --agent --yes
assert test -f /etc/ctlvps-proxy/public/config.json
assert test -d /var/lib/ctlvps-proxy/public/acme
assert test ! -f /etc/systemd/system/ctlvps-singbox-private.service
assert test ! -f /etc/systemd/system/ctlvps-proxy-public.slice
assert test ! -f /etc/systemd/system/ctlvps-proxy-guard.service
assert test ! -f /etc/systemd/system/ctlvps-proxy-guard.timer
assert systemctl is-active --quiet ctlvpsd
uninstall --agent --purge --yes
assert test ! -d /etc/ctlvps-proxy
assert test ! -d /var/lib/ctlvps-proxy
done_case 'isolated proxy fixed launchers, slices, preserved state and explicit purge'

printf 'Uninstaller integration: %s scenarios passed (real systemd and nftables)\n' "$checks"
