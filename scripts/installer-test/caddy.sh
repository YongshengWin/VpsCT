#!/usr/bin/env bash
set -euo pipefail
cd /src
asset=$(find /assets -maxdepth 1 -name '*-linux-*.tar.gz' -print -quit)
version=$(tar -xOf "$asset" VERSION | tr -d '\n')
repo=$(tar -xOf "$asset" REPOSITORY | tr -d '\n')
args=(--repo "$repo" --version "$version" --assets-dir /assets --domain panel.example.test)
mkdir /etc/caddy
printf '# Existing user configuration\n' > /etc/caddy/Caddyfile
if bash install.sh "${args[@]}" > /tmp/existing-proxy.log 2>&1; then
  echo 'Existing Caddy installation was overwritten' >&2; exit 1
fi
grep -q '^# Existing user configuration$' /etc/caddy/Caddyfile
grep -q '检测到现有 Caddy' /tmp/existing-proxy.log
rm /etc/caddy/Caddyfile
rmdir /etc/caddy
bash install.sh "${args[@]}"
caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile
grep -q '^panel.example.test {' /etc/caddy/Caddyfile
grep -q 'reverse_proxy 127.0.0.1:8080' /etc/caddy/Caddyfile
curl -fsS http://127.0.0.1:8080/healthz >/dev/null
systemctl stop ctlvpsd
printf 'Caddy branch passed: existing config protection, official package installation and config validation (no public certificate issued).\n'
