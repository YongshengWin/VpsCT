#!/usr/bin/env bash
set -euo pipefail
cd /src
target_arch=$(uname -m)
case "$target_arch" in aarch64) target_arch=arm64;; x86_64) target_arch=amd64;; *) exit 1;; esac
asset=$(find /assets -maxdepth 1 -name "*-linux-$target_arch.tar.gz" -print -quit)
version=$(tar -xOf "$asset" VERSION | tr -d '\n')
repo=$(tar -xOf "$asset" REPOSITORY | tr -d '\n')
args=(--repo "$repo" --version "$version" --assets-dir /assets)
python3 /src/scripts/security-fixture.py init
python3 /src/scripts/security-fixture.py sign controller "$version" "$asset"
bash install.sh "${args[@]}" --site-url https://panel.example.test --no-proxy
old_release=$(readlink -f /opt/ctlvps/current)
old_config=$(sha256sum /etc/ctlvps/ctlvpsd.env)
python3 - <<'PY'
import json, pathlib, urllib.request, urllib.error
base='http://127.0.0.1:8080'
def setup(token):
    body=json.dumps({'username':'test-admin','password':'fixture-only-password','setup_token':token}).encode()
    try:
        with urllib.request.urlopen(urllib.request.Request(base+'/api/v1/auth/setup',data=body,headers={'Content-Type':'application/json','Origin':'https://panel.example.test','Host':'panel.example.test'})) as r:
            return r.status
    except urllib.error.HTTPError as e: return e.code
assert setup('wrong') == 403
path=pathlib.Path('/opt/ctlvps/data/setup-token')
assert path.stat().st_mode & 0o777 == 0o600
assert setup(path.read_text().strip()) == 200
assert not path.exists()
PY
for arch in amd64 arm64; do
  curl -fsS -H "Host: panel.example.test" "http://127.0.0.1:8080/dl/agent/linux-$arch" -o "/tmp/download-$arch"
  cmp "/tmp/download-$arch" "/opt/ctlvps/agents/ctlvps-agent-linux-$arch"
done
# A repeated fresh install must never overwrite data or config.
if bash install.sh "${args[@]}" --site-url https://other.example.test --no-proxy >/dev/null 2>&1; then
  echo 'Existing installation was overwritten' >&2; exit 1
fi
[[ "$(sha256sum /etc/ctlvps/ctlvpsd.env)" == "$old_config" ]]
# A corrupt release must be rejected before the running version changes.
mkdir /bad-assets
cp /assets/* /bad-assets/
printf 'corruption' >> "/bad-assets/$(basename "$asset")"
# Corrupt both architectures so this works on either host architecture.
for other in /bad-assets/*-linux-*.tar.gz; do printf 'corruption' >> "$other"; done
if bash install.sh --repo "$repo" --version "$version" --assets-dir /bad-assets --update >/dev/null 2>&1; then
  echo 'Corrupt package was accepted' >&2; exit 1
fi
[[ "$(readlink -f /opt/ctlvps/current)" == "$old_release" ]]
curl -fsS -H "Host: panel.example.test" http://127.0.0.1:8080/healthz >/dev/null
# Upgrade and check that the administrator and custom config survive.
bash install.sh "${args[@]}" --update
[[ "$(sha256sum /etc/ctlvps/ctlvpsd.env)" == "$old_config" ]]
[[ "$(readlink -f /opt/ctlvps/current)" != "$old_release" ]]
[[ -f /opt/ctlvps/agents/ctlvps-agent-linux-amd64 && -f /opt/ctlvps/agents/ctlvps-agent-linux-arm64 ]]
python3 - <<'PY'
import json, pathlib, tarfile, urllib.request
with urllib.request.urlopen(urllib.request.Request('http://127.0.0.1:8080/api/v1/auth/setup',headers={'Host':'panel.example.test'})) as r:
    assert json.load(r)['needs_setup'] is False
backups=list(pathlib.Path('/opt/ctlvps/backups').glob('*/data.tar.gz.enc'))
assert len(backups)==1
import subprocess
subprocess.run(['/usr/local/libexec/ctlvps-verify','backup','open','/etc/ctlvps/secrets.key',str(backups[0]),'/tmp/restored-check.tar.gz'],check=True)
with tarfile.open('/tmp/restored-check.tar.gz') as t: assert 'data/ctlvps.db' in t.getnames()
assert not pathlib.Path('/opt/ctlvps/data/setup-token').exists()
PY
systemctl stop ctlvpsd
systemctl start ctlvpsd

# Reject a downgrade before changing a version or reading its release files.
if bash install.sh --repo "$repo" --version v0.0.0-alpha --assets-dir /assets --update > /tmp/downgrade.log 2>&1; then
  echo 'Downgrade was accepted' >&2; exit 1
fi
grep -q '不支持直接降级' /tmp/downgrade.log

# A verified package whose daemon fails after installation must leave the
# service stopped and preserve a restorable data/config/version snapshot.
python3 - <<'PY'
import hashlib, pathlib, tarfile, tempfile, shutil
source=next(pathlib.Path('/assets').glob('*-linux-*.tar.gz'))
out=pathlib.Path('/broken-assets');out.mkdir()
with tempfile.TemporaryDirectory() as directory:
    directory=pathlib.Path(directory)
    with tarfile.open(source) as t: t.extractall(directory, filter='data')
    major,minor,patch=map(int,(directory/'VERSION').read_text().strip().removeprefix('v').split('-',1)[0].split('.'))
    broken_version=f'v{major}.{minor}.{patch+1}-installer-test'
    (directory/'VERSION').write_text(broken_version+'\n')
    (out/'VERSION').write_text(broken_version+'\n')
    (directory/'ctlvpsd').write_text('#!/bin/sh\nif [ "${1:-}" = version ]; then echo "ctlvpsd test failure fixture"; exit 0; fi\nexit 1\n')
    (directory/'ctlvpsd').chmod(0o755)
    for arch in ('amd64','arm64'):
        target=out/f'ctlvps-{broken_version}-linux-{arch}.tar.gz'
        with tarfile.open(target,'w:gz') as t:
            for path in sorted(directory.iterdir()): t.add(path,arcname=path.name)
for helper in pathlib.Path('/assets').glob('ctlvps-verify-linux-*'): shutil.copyfile(helper,out/helper.name)
with (out/'SHA256SUMS').open('w') as f:
    for path in sorted(p for p in out.iterdir() if p.name != 'SHA256SUMS'): f.write(f'{hashlib.sha256(path.read_bytes()).hexdigest()}  {path.name}\n')
PY
broken_version=$(cat /broken-assets/VERSION)
arch=$(uname -m); [[ "$arch" != aarch64 ]] || arch=arm64; [[ "$arch" != x86_64 ]] || arch=amd64
python3 /src/scripts/security-fixture.py sign controller "$broken_version" "/broken-assets/ctlvps-$broken_version-linux-$arch.tar.gz"
if bash install.sh --repo "$repo" --version "$broken_version" --assets-dir /broken-assets --update > /tmp/failed-upgrade.log 2>&1; then
  echo 'Broken daemon reported success' >&2; exit 1
fi
if systemctl is-active --quiet ctlvpsd; then echo 'Broken service left running' >&2; exit 1; fi
grep -q '启动健康检查失败' /tmp/failed-upgrade.log
backup=$(find /opt/ctlvps/backups -mindepth 1 -maxdepth 1 -type d | sort | tail -1)
[[ -f "$backup/data.tar.gz.enc" && -f "$backup/previous-release" ]]
# Exercise the documented restore procedure against the actual backup.
mv /opt/ctlvps/data /opt/ctlvps/data.failed-test
/usr/local/libexec/ctlvps-verify backup open /etc/ctlvps/secrets.key "$backup/data.tar.gz.enc" /tmp/restore.tar.gz
tar -xzf /tmp/restore.tar.gz -C /opt/ctlvps
install -m 0600 "$backup/ctlvpsd.env" /etc/ctlvps/ctlvpsd.env
install -m 0644 "$backup/ctlvpsd.service" /etc/systemd/system/ctlvpsd.service
install -m 0644 "$(cat "$backup/previous-release")/REPOSITORY" /opt/ctlvps/REPOSITORY
ln -s "$(cat "$backup/previous-release")" /opt/ctlvps/current.restore
mv -Tf /opt/ctlvps/current.restore /opt/ctlvps/current
systemctl daemon-reload
systemctl start ctlvpsd
for ((attempt = 0; attempt < 30; attempt++)); do
  if curl -fsS -H "Host: panel.example.test" http://127.0.0.1:8080/healthz >/dev/null; then break; fi
  sleep 1
done
curl -fsS -H "Host: panel.example.test" http://127.0.0.1:8080/api/v1/auth/setup | python3 -c 'import json,sys; assert not json.load(sys.stdin)["needs_setup"]'
[[ "$(sha256sum /etc/ctlvps/ctlvpsd.env)" == "$old_config" ]]
systemctl stop ctlvpsd
printf 'Installer integration passed: install, setup, agent downloads, repeat protection, corrupt package, upgrade, downgrade rejection, failed upgrade and restore.\n'
