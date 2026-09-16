"""Actual v0.1.1 controller -> candidate, including failure rollback."""
import hashlib,json,pathlib,platform,shutil,subprocess,tarfile,time,urllib.request,urllib.error
P=pathlib.Path
assert P('/.dockerenv').exists()
arch={'aarch64':'arm64','x86_64':'amd64'}[platform.machine()]
def run(*a,check=True):
 r=subprocess.run(a,text=True,capture_output=True)
 if check and r.returncode:raise RuntimeError((a[0],r.stdout[-1800:],r.stderr[-1800:]))
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
cookie=''
def request(path,body=None):
 global cookie
 data=None if body is None else json.dumps(body).encode()
 headers={'Host':'panel.fixture.test','Origin':'https://panel.fixture.test','Content-Type':'application/json','Cookie':cookie}
 if cookie and body is not None and path not in ('/api/v1/auth/setup','/api/v1/auth/login'):headers['X-CSRF-Token']=request('/api/v1/auth/csrf')['token']
 req=urllib.request.Request('http://127.0.0.1:8080'+path,data=data,headers=headers)
 with urllib.request.urlopen(req,timeout=5) as r:
  if r.headers.get('Set-Cookie'):cookie=r.headers['Set-Cookie'].split(';',1)[0]
  return json.load(r)
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

# Check the default verifier against the real published official HTTPS catalog.
legacy_agent=P('/tmp/legacy-controller.tar.gz')
shutil.copyfile(old,legacy_agent)
run('/usr/local/libexec/ctlvps-verify','verify-release','controller','v0.1.1',str(legacy_agent))
legacy_agent.write_bytes(legacy_agent.read_bytes()+b'tamper')
assert run('/usr/local/libexec/ctlvps-verify','verify-release','controller','v0.1.1',str(legacy_agent),check=False).returncode!=0
print('OFFICIAL HTTPS PASS: published controller accepted; altered bytes rejected without signing keys.',flush=True)

# Real agent enrollment over test TLS; only fixed GitHub release downloads are
# mapped to the local candidate assets. No publisher key or metadata exists.
import http.client,ssl,threading,os
from http.server import BaseHTTPRequestHandler,ThreadingHTTPServer
run('openssl','req','-x509','-newkey','rsa:2048','-nodes','-keyout','/tmp/agent-test-tls.key','-out','/usr/local/share/ca-certificates/keyless-agent-test.crt','-days','1','-subj','/CN=127.0.0.1','-addext','subjectAltName=IP:127.0.0.1')
run('update-ca-certificates')
class Bridge(BaseHTTPRequestHandler):
 def log_message(self,*args):pass
 def do_GET(self):self.forward()
 def do_POST(self):self.forward()
 def forward(self):
  body=self.rfile.read(int(self.headers.get('Content-Length','0')))
  conn=http.client.HTTPConnection('127.0.0.1',8080,timeout=20)
  headers=dict(self.headers);headers["Host"]="panel.fixture.test"
  conn.request(self.command,self.path,body,headers)
  r=conn.getresponse();data=r.read();self.send_response(r.status)
  self.send_header('Content-Length',str(len(data)));self.send_header('Content-Type','application/json');self.end_headers();self.wfile.write(data);conn.close()
bridge=ThreadingHTTPServer(('127.0.0.1',9443),Bridge)
tls=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER);tls.load_cert_chain('/usr/local/share/ca-certificates/keyless-agent-test.crt','/tmp/agent-test-tls.key')
bridge.socket=tls.wrap_socket(bridge.socket,server_side=True)
threading.Thread(target=bridge.serve_forever,daemon=True).start()
request('/api/v1/auth/setup',{'username':'agent-fixture','password':'fixture-password-only','setup_token':P('/opt/ctlvps/data/setup-token').read_text().strip()})
server=request('/api/v1/servers',{'name':'keyless-agent-fixture'})
enroll=request(f"/api/v1/servers/{server['id']}/enroll-token",{})
P('/usr/local/bin/curl').write_text("""#!/usr/bin/python3
import os,sys,pathlib,shutil
args=sys.argv[1:]
urls=[s for s in args if s.startswith('https://github.com/YongshengWin/VpsCT/releases/download/')]
if urls:
 name=urls[0].rsplit('/',1)[1]
 source=pathlib.Path('/fixtures/agent-overrides')/name
 if not source.exists():source=pathlib.Path('/assets')/name
 shutil.copyfile(source,args[args.index('-o')+1]);sys.exit(0)
os.execv('/usr/bin/curl',['/usr/bin/curl']+args)
""")
P('/usr/local/bin/curl').chmod(0o755)
agent_script=P('/fixtures/install-agent.sh')
agent_script.write_text(P('/src/internal/assets/install-agent.sh').read_text().replace("RELEASE_VERSION='__VERSION__'",f"RELEASE_VERSION='{version}'"))
# Remove controller-provided helper to prove the agent installer supplies it.
P('/usr/local/libexec/ctlvps-verify').unlink()
run('bash',str(agent_script),'--server','https://127.0.0.1:9443','--token',enroll['token'])
assert P('/usr/local/libexec/ctlvps-verify').is_file()
assert P('/var/lib/ctlvps-agent/state.json').is_file()
assert run('systemctl','is-active','ctlvps-agent').stdout.strip()=='active'
assert not P('/etc/ctlvps/security.json').exists()
print('AGENT PASS: actual agent enrolled and started with automatic helper, no publisher keys.',flush=True)
run('bash',str(agent_script),'--server','https://127.0.0.1:9443','--update')
assert run('systemctl','is-active','ctlvps-agent').stdout.strip()=='active'
print('AGENT UPDATE PASS: terminal update works without signing setup.',flush=True)

# A checksum-valid but non-starting candidate must restore the previous agent.
overrides=P('/fixtures/agent-overrides');overrides.mkdir()
broken_agent=overrides/f'ctlvps-agent-linux-{arch}'
broken_agent.write_text('#!/bin/sh\nif [ "${1:-}" = version ]; then echo broken-fixture; exit 0; fi\nexit 1\n')
sums=(P('/assets')/'SHA256SUMS').read_text().splitlines()
sums=[hashlib.sha256(broken_agent.read_bytes()).hexdigest()+'  '+broken_agent.name if line.split()[-1]==broken_agent.name else line for line in sums]
(overrides/'SHA256SUMS').write_text('\n'.join(sums)+'\n')
old_digest=hashlib.sha256(P('/usr/local/bin/ctlvps-agent').read_bytes()).hexdigest()
failed=run('bash',str(agent_script),'--server','https://127.0.0.1:9443','--update',check=False)
assert failed.returncode!=0
assert hashlib.sha256(P('/usr/local/bin/ctlvps-agent').read_bytes()).hexdigest()==old_digest
assert run('systemctl','is-active','ctlvps-agent').stdout.strip()=='active'
print('AGENT ROLLBACK PASS: failed terminal update restored previous running binary.',flush=True)
