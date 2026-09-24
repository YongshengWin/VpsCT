#!/usr/bin/env bash
set -euo pipefail
# Disposable installer-test container only: /src, /old-assets and /assets
# must be read-only mounts. Never run this script on a VPS or the host.
[[ -f /.dockerenv && -d /old-assets && -d /assets ]] || exit 1
cd /src
arch=$(uname -m); [[ "$arch" != aarch64 ]] || arch=arm64
python3 scripts/security-fixture.py init
python3 scripts/security-fixture.py sign controller v0.1.4 "/old-assets/ctlvps-v0.1.4-linux-$arch.tar.gz"
bash install.sh --repo YongshengWin/VpsCT --version v0.1.4 --assets-dir /old-assets --site-url https://panel.example.test --no-proxy >/tmp/install-old.log 2>&1
python3 - <<'PY'
import json,pathlib,urllib.request,sqlite3
p=pathlib.Path('/opt/ctlvps/data')
body=json.dumps(dict(username='upgrade-fixture',password='fixture-only-password',setup_token=(p/'setup-token').read_text().strip())).encode()
r=urllib.request.urlopen(urllib.request.Request('http://127.0.0.1:8080/api/v1/auth/setup',data=body,headers={'Content-Type':'application/json','Origin':'https://panel.example.test','Host':'panel.example.test'}))
assert r.status==200
c=sqlite3.connect(p/'ctlvps.db');assert c.execute('select version from schema_version').fetchone()[0]==11
pathlib.Path('/tmp/account-before.json').write_text(json.dumps(c.execute('select id,username,password_hash,role from users').fetchall()))
print('PASS genuine v0.1.4 package initialized at schema 11')
PY
python3 scripts/security-fixture.py sign controller v0.1.5 "/assets/ctlvps-v0.1.5-linux-$arch.tar.gz"
bash install.sh --repo YongshengWin/VpsCT --version v0.1.5 --assets-dir /assets --update --auto-rollback >/tmp/upgrade-new.log 2>&1
python3 - <<'PY'
import json,pathlib,sqlite3,urllib.request
p=pathlib.Path('/opt/ctlvps/data');c=sqlite3.connect(p/'ctlvps.db')
assert c.execute('select version from schema_version').fetchone()[0]==30
assert json.loads(pathlib.Path('/tmp/account-before.json').read_text())==[list(r) for r in c.execute('select id,username,password_hash,role from users')]
assert not (p/'setup-token').exists()
assert list(pathlib.Path('/opt/ctlvps/backups').glob('*/data.tar.gz.enc'))
with urllib.request.urlopen(urllib.request.Request('http://127.0.0.1:8080/api/v1/auth/setup',headers={'Host':'panel.example.test'})) as r:assert not json.load(r)['needs_setup']
print('PASS v0.1.4 -> v0.1.5 installer upgrade: schema 11 -> 30, account preserved, setup remains closed, encrypted backup exists')
PY
systemctl stop ctlvpsd

# Restore the pre-upgrade binary, environment and encrypted data together.
backup=$(find /opt/ctlvps/backups -mindepth 1 -maxdepth 1 -type d | sort | tail -1)
/usr/local/libexec/ctlvps-verify backup open /etc/ctlvps/secrets.key "$backup/data.tar.gz.enc" /tmp/previous-data.tar.gz
mv /opt/ctlvps/data /opt/ctlvps/data.after-upgrade
tar -xzf /tmp/previous-data.tar.gz -C /opt/ctlvps
install -m 0600 "$backup/ctlvpsd.env" /etc/ctlvps/ctlvpsd.env
install -m 0644 "$backup/ctlvpsd.service" /etc/systemd/system/ctlvpsd.service
ln -s "$(cat "$backup/previous-release")" /opt/ctlvps/current.restore
mv -Tf /opt/ctlvps/current.restore /opt/ctlvps/current
systemctl daemon-reload
systemctl start ctlvpsd
python3 - <<'RESTORE'
import json,pathlib,sqlite3,time,urllib.request
for attempt in range(30):
 try:
  with urllib.request.urlopen(urllib.request.Request('http://127.0.0.1:8080/api/v1/auth/setup',headers={'Host':'panel.example.test'})) as r: assert not json.load(r)['needs_setup']
  break
 except OSError:
  time.sleep(1)
else: raise AssertionError('restored v0.1.4 did not start')
c=sqlite3.connect('/opt/ctlvps/data/ctlvps.db')
assert c.execute('select version from schema_version').fetchone()[0]==11
assert json.loads(pathlib.Path('/tmp/account-before.json').read_text())==[list(r) for r in c.execute('select id,username,password_hash,role from users')]
print('PASS restore pre-upgrade v0.1.4 binary and encrypted backup: schema 11, original administrator, setup closed')
RESTORE
systemctl stop ctlvpsd
