"""Actual v0.1.1 controller -> candidate, including failure rollback."""
import hashlib,json,pathlib,platform,shutil,subprocess,tarfile,time,urllib.request,urllib.error
P=pathlib.Path
assert P('/.dockerenv').exists()
arch={'aarch64':'arm64','x86_64':'amd64'}[platform.machine()]
def run(*a,check=True):
 r=subprocess.run(a,text=True,capture_output=True)
 if check and r.returncode:raise RuntimeError((a,r.stdout[-1800:],r.stderr[-1800:]))
 return r
# Only package management is stubbed. PID1, application, installer and verifier are real.
P('/usr/local/bin/apt-get').write_text('#!/bin/sh\nexit 0\n');P('/usr/local/bin/apt-get').chmod(0o755)
old=P('/legacy')/f'ctlvps-v0.1.1-linux-{arch}.tar.gz'
new=next(P('/assets').glob(f'ctlvps-*-linux-{arch}.tar.gz'))
work=P('/fixtures/upgrade');work.mkdir(parents=True)
helper=P("/assets")/f"ctlvps-verify-linux-{arch}"
for p in [old,new,helper]:shutil.copyfile(p,work/p.name)
with tarfile.open(new) as t:version=t.extractfile('VERSION').read().decode().strip()
P(work/'SHA256SUMS').write_text(''.join(hashlib.sha256(p.read_bytes()).hexdigest()+'  '+p.name+'\n' for p in work.iterdir() if p.is_file()))
run('bash','/legacy/install.sh','--version','v0.1.1','--repo','YongshengWin/VpsCT','--assets-dir',str(work),'--site-url','https://panel.fixture.test','--no-proxy')
def request(path,body=None):
 data=None if body is None else json.dumps(body).encode()
 req=urllib.request.Request('http://127.0.0.1:8080'+path,data=data,headers={'Host':'panel.fixture.test','Origin':'https://panel.fixture.test','Content-Type':'application/json'})
 with urllib.request.urlopen(req,timeout=5) as r:return json.load(r)
request('/api/v1/auth/setup',{'username':'upgrade-fixture','password':'fixture-password-only','setup_token':P('/opt/ctlvps/data/setup-token').read_text().strip()})
assert request('/api/v1/auth/setup')['needs_setup'] is False
assert 'v0.1.1' in run('/opt/ctlvps/ctlvpsd','version').stdout
print('LEGACY PASS: actual published v0.1.1 installed and administrator created.',flush=True)
assert not P('/etc/ctlvps/security.json').exists()
assert not P('/usr/local/libexec/ctlvps-verify').exists()
# Produce a checksum-verified but non-starting fixture to exercise the real old-version rollback.
bad='v0.1.2-upgrade-failure'
bad_dir=work/'bad';bad_dir.mkdir()
with tarfile.open(new) as t:t.extractall(bad_dir,filter='data')
(bad_dir/'VERSION').write_text(bad+'\n')
(bad_dir/'ctlvpsd').write_text('#!/bin/sh\nif [ "${1:-}" = version ]; then echo fixture; exit 0; fi\nexit 1\n');(bad_dir/'ctlvpsd').chmod(0o755)
bad_tar=work/f'ctlvps-{bad}-linux-{arch}.tar.gz'
with tarfile.open(bad_tar,'w:gz') as t:
 for p in bad_dir.iterdir():t.add(p,arcname=p.name)
with (work/'SHA256SUMS').open('a') as f:f.write(hashlib.sha256(bad_tar.read_bytes()).hexdigest()+'  '+bad_tar.name+'\n')

base=['bash','/assets/install.sh','--repo','YongshengWin/VpsCT','--assets-dir',str(work),'--update','--auto-rollback']
helper_copy=work/helper.name
original_helper=helper_copy.read_bytes()
helper_copy.write_bytes(b'corrupt helper')
unsafe=run(*base,'--version',version,check=False)
helper_copy.write_bytes(original_helper)
assert unsafe.returncode!=0
assert 'v0.1.1' in run('/opt/ctlvps/ctlvpsd','version').stdout
assert run('systemctl','is-active','ctlvpsd.service').stdout.strip()=='active'
print('PREFLIGHT PASS: corrupt helper rejected without stopping the old service.',flush=True)
failed=run(*base,'--version',bad,check=False)
assert failed.returncode!=0
assert 'v0.1.1' in run('/opt/ctlvps/ctlvpsd','version').stdout
assert request('/api/v1/auth/setup')['needs_setup'] is False
assert run('systemctl','is-active','ctlvpsd.service').stdout.strip()=='active'
print('ROLLBACK PASS: checksum-verified failing candidate restored actual v0.1.1 program and administrator data.',flush=True)
run(*base,'--version',version)
assert version in run('/opt/ctlvps/ctlvpsd','version').stdout
assert request('/api/v1/auth/setup')['needs_setup'] is False
assert list(P('/opt/ctlvps/backups').glob('*/data.tar.gz.enc'))
assert P('/usr/local/libexec/ctlvps-install.sh').is_file()
assert P('/usr/local/libexec/ctlvps-install-agent.sh').is_file()
run('systemctl','restart','ctlvpsd.service')
for _ in range(50):
 try:
  assert request('/api/v1/auth/setup')['needs_setup'] is False;break
 except (OSError,urllib.error.URLError):time.sleep(.1)
else:raise AssertionError('candidate did not recover after restart')
print('UPGRADE PASS: actual v0.1.1 -> candidate; schema migration, existing administrator, encrypted backup, local installers and restart verified.',flush=True)

assert not P("/etc/ctlvps/security.json").exists()
assert not P("/fixtures/security/keys").exists()
print("KEYLESS PASS: no publisher keys, trust root, metadata server or preinstalled verifier.",flush=True)

# A fresh controller install must also work without publisher configuration.
run('bash','/src/uninstall.sh','--controller','--purge','--yes')
run('bash','/assets/install.sh','--repo','YongshengWin/VpsCT','--assets-dir',str(work),'--site-url','https://panel.fixture.test','--no-proxy')
assert request('/api/v1/auth/setup')['needs_setup'] is True
assert not P('/etc/ctlvps/security.json').exists()
print('FRESH PASS: new controller installed without signing keys or policy.',flush=True)
