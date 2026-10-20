"""Real legacy web upgrade and unchanged subscription traffic in an offline VM.

Only GitHub release transport is substituted. The default checksum policy,
HTTPS validation, historical application, systemd, agent and client are real.
All credentials and endpoints are synthetic and confined to this container.
"""
import hashlib
import http.client
import json
import os
import pathlib
import platform
import shutil
import ssl
import subprocess
import tarfile
import threading
import time
import urllib.error
import urllib.request
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

P = pathlib.Path
assert P('/.dockerenv').exists() and P('/proc/1/comm').read_text().strip() == 'systemd'
arch = {'aarch64': 'arm64', 'x86_64': 'amd64'}[platform.machine()]
root = P('/opt/ctlvps')
base = 'http://127.0.0.1:8080'
site = 'https://127.0.0.1:9443'
password = 'offline-web-upgrade-fixture-password'
cookie = ''
latest = None

def run(*args, check=True):
    result = subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
    if check and result.returncode:
        raise AssertionError((args[0], result.returncode, result.stdout[-1500:]))
    return result

def wait(check, label, timeout=150):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            result = check()
            if result:
                return result
        except (OSError, ValueError, urllib.error.URLError):
            pass
        time.sleep(.4)
    raise AssertionError('timed out: ' + label)

def request(path, payload=None):
    global cookie
    headers = {'Host': '127.0.0.1:9443', 'Origin': site, 'Content-Type': 'application/json', 'Cookie': cookie}
    if payload is not None and cookie:
        headers['X-CSRF-Token'] = request('/api/v1/auth/csrf')['token']
    body = None if payload is None else json.dumps(payload).encode()
    req = urllib.request.Request(base + path, data=body, headers=headers)
    with urllib.request.urlopen(req, timeout=10) as response:
        if response.headers.get('Set-Cookie'):
            cookie = response.headers['Set-Cookie'].split(';', 1)[0]
        return json.load(response)

def tag(directory):
    asset = next(P(directory).glob(f'ctlvps-*-linux-{arch}.tar.gz'))
    with tarfile.open(asset) as archive:
        return archive.extractfile('VERSION').read().decode().strip()

old_version, version = tag('/legacy'), tag('/assets')
assert old_version != version, 'test requires actual different historical and candidate packages'
with tarfile.open(next(P('/assets').glob(f'ctlvps-*-linux-{arch}.tar.gz'))) as archive:
    controller_sha = hashlib.file_digest(archive.extractfile('ctlvpsd'), 'sha256').hexdigest()
assert '1.14.1' in run('/fixtures/sing-box', 'version').stdout
# Fake the fixed official release endpoints inside a network-less container.
# A globally unicast fixture address exercises the real outbound address checks.
run('ip', 'addr', 'add', '93.184.216.34/32', 'dev', 'lo')
with P('/etc/hosts').open('a') as stream:
    stream.write('\n93.184.216.34 github.com\n93.184.216.35 proxy-target.fixture\n')
run('openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1',
    '-subj', '/CN=github.com', '-addext', 'subjectAltName=DNS:github.com,IP:127.0.0.1,DNS:proxy-target.fixture',
    '-keyout', '/tmp/fixture.key', '-out', '/usr/local/share/ca-certificates/web-upgrade-fixture.crt')
run('update-ca-certificates')

class Catalog(BaseHTTPRequestHandler):
    def log_message(self, *_): pass
    def do_GET(self):
        prefix = '/YongshengWin/VpsCT/releases/'
        if not self.path.startswith(prefix):
            self.send_error(404); return
        tail = self.path[len(prefix):]
        if tail.startswith('latest/download/'):
            directory, name = latest, tail[len('latest/download/'):]
        elif tail.startswith('download/'):
            release, name = tail[len('download/'):].split('/', 1)
            directory = {'/': None, old_version: '/legacy', version: '/assets'}.get(release)
        else:
            self.send_error(404); return
        if directory is None or '/' in name:
            self.send_error(404); return
        path = P(directory) / name
        if not path.is_file():
            self.send_error(404); return
        self.send_response(200)
        self.send_header('Content-Length', str(path.stat().st_size)); self.end_headers()
        with path.open('rb') as stream: shutil.copyfileobj(stream, self.wfile)

def serve(address, handler):
    server = ThreadingHTTPServer(address, handler)
    tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    tls.load_cert_chain('/usr/local/share/ca-certificates/web-upgrade-fixture.crt', '/tmp/fixture.key')
    server.socket = tls.wrap_socket(server.socket, server_side=True)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server

latest = '/assets'
serve(('93.184.216.34', 443), Catalog)
P('/usr/local/bin/apt-get').write_text('#!/bin/sh\nexit 0\n')
P('/usr/local/bin/apt-get').chmod(0o755)
# Installer curl and the Go isolated downloader both use the HTTPS catalog.
run('bash', '/legacy/install.sh', '--assets-dir', '/legacy', '--version', old_version,
    '--repo', 'YongshengWin/VpsCT', '--site-url', site, '--no-proxy')
wait(lambda: request('/healthz'), 'legacy controller startup')
request('/api/v1/auth/setup', {'username': 'web-upgrade-fixture', 'password': password,
                             'setup_token': (root / 'data/setup-token').read_text().strip()})
assert not P('/etc/ctlvps/security.json').exists(), 'default checksum policy required'

class Bridge(BaseHTTPRequestHandler):
    def log_message(self, *_): pass
    def do_GET(self): self.forward()
    def do_POST(self): self.forward()
    def forward(self):
        conn = http.client.HTTPConnection('127.0.0.1', 8080, timeout=20)
        body = self.rfile.read(int(self.headers.get('Content-Length', '0')))
        conn.request(self.command, self.path, body, dict(self.headers))
        response = conn.getresponse(); data = response.read()
        self.send_response(response.status)
        self.send_header('Content-Length', str(len(data))); self.end_headers(); self.wfile.write(data)
        conn.close()

serve(('127.0.0.1', 9443), Bridge)
server = request('/api/v1/servers', {'name': 'offline-managed-fixture', 'public_host': '127.0.0.1'})
sid = server['id']
enroll = request(f'/api/v1/servers/{sid}/enroll-token', {})
# Avoid kernel downloads; use the caller's verified official file and receipt.
P('/opt/ctlvps/bin').mkdir(parents=True, exist_ok=True)
shutil.copyfile('/fixtures/sing-box', '/opt/ctlvps/bin/sing-box')
P('/opt/ctlvps/bin/sing-box').chmod(0o755)
digest = hashlib.sha256(P('/opt/ctlvps/bin/sing-box').read_bytes()).hexdigest()
P('/opt/ctlvps/bin/sing-box.trusted').write_text(json.dumps({'Version': '1.14.1', 'SHA256': digest}))
run('bash', '/legacy/install-agent.sh', '--server', site, '--token', enroll['token'])
wait(lambda: request(f'/api/v1/servers/{sid}/maintenance')['available'], 'legacy agent maintenance')
node = request(f'/api/v1/servers/{sid}/nodes', {'protocol': 'ss', 'name': 'upgrade-session', 'port': 25400})
template = request('/api/v1/templates', {'name': 'offline-client', 'kind': 'singbox', 'content':
    '{"inbounds":[{"type":"socks","listen":"127.0.0.1","listen_port":1080}],'
    '"outbounds":[{"type":"selector","tag":"selection","outbounds":["{{all}}"]}],"route":{"final":"selection"}}'})
sub = request('/api/v1/subscriptions', {'name': 'unchanged-subscription', 'kind': 'generated',
    'template_id': template['id'], 'node_selection': {'node_ids': [node['id']]}})
sub_path = '/s/' + sub['token'] + '/singbox'
# Keep the business target on a separate network namespace: a public core
# intentionally refuses all addresses belonging to its own machine.
run('ip', 'netns', 'add', 'target')
run('ip', 'link', 'add', 'wan-fixture', 'type', 'veth', 'peer', 'name', 'peer-fixture')
run('ip', 'link', 'set', 'peer-fixture', 'netns', 'target')
run('ip', 'addr', 'add', '192.0.2.1/24', 'dev', 'wan-fixture')
run('ip', 'link', 'set', 'wan-fixture', 'up')
for args in [('addr', 'add', '192.0.2.2/24', 'dev', 'peer-fixture'),
             ('link', 'set', 'peer-fixture', 'up'), ('link', 'set', 'lo', 'up'),
             ('addr', 'add', '93.184.216.35/32', 'dev', 'lo'),
             ('route', 'add', 'default', 'via', '192.0.2.1')]:
    run('ip', 'netns', 'exec', 'target', 'ip', *args)
run('ip', 'route', 'add', '93.184.216.35/32', 'via', '192.0.2.2', 'dev', 'wan-fixture')
subprocess.Popen(['ip', 'netns', 'exec', 'target', 'python3', '/src/scripts/web-upgrade-test/target.py'])

def subscription_bytes():
    req = urllib.request.Request(base + sub_path, headers={'Host': '127.0.0.1:9443', 'User-Agent': 'sing-box'})
    with urllib.request.urlopen(req, timeout=10) as response: return response.read()

def applied(candidate=False):
    details = request(f'/api/v1/servers/{sid}')
    agent = details.get('agent') or {}
    desired = details.get('desired') or {}
    ready = agent.get('applied_revision', 0) > 0 and not agent.get('apply_error') and agent.get('applied_revision') == desired.get('revision')
    if candidate:
        cores = (details.get('diagnostics') or {}).get('cores') or []
        ready = ready and any(c.get('name') == 'sing-box' and c.get('wanted') and c.get('active') for c in cores)
        ready = ready and (details.get('agent_update') or {}).get('current_sha') == target_sha
        ready = ready and not any(j['status'] in ('queued', 'running') for j in request(f'/api/v1/servers/{sid}/maintenance')['jobs'])
    return ready

def probe(label, expected=None):
    data = subscription_bytes()
    if expected is not None: assert data == expected, 'upgrade changed the existing subscription parameters'
    doc = json.loads(data)
    assert any(out.get('type') == 'shadowsocks' for out in doc['outbounds']), 'managed node missing'
    P('/tmp/exported-client.json').write_bytes(data)
    log = P('/tmp/subscription-client.log').open('w')
    client = subprocess.Popen(['/fixtures/sing-box', 'run', '-c', '/tmp/exported-client.json'],
                              stdout=log, stderr=log)
    try:
        for scheme, port in [('http', 10080), ('https', 10443)]:
            def fetch():
                result = run('/usr/bin/curl', '--silent', '--show-error', '--max-time', '4',
                    '--noproxy', '', '--socks5-hostname', '127.0.0.1:1080',
                    '--cacert', '/usr/local/share/ca-certificates/web-upgrade-fixture.crt',
                    '-o', '/dev/null', '-w', '%{http_code}', f'{scheme}://proxy-target.fixture:{port}/', check=False)
                return result.returncode == 0 and result.stdout == '204'
            try:
                wait(fetch, label + ' domain ' + scheme, 30)
            except AssertionError:
                print('Client diagnostics:', P('/tmp/subscription-client.log').read_text()[-3500:], flush=True)
                logs = P('/var/log/ctlvps/sing-box.log')
                if logs.exists(): print('Server diagnostics:', logs.read_text()[-3500:], flush=True)
                raise
    finally:
        client.terminate(); client.wait(timeout=5); log.close()
    print('PASS', label, 'unchanged subscription, domain HTTP/HTTPS through official client', flush=True)
    return data

wait(applied, 'legacy node application')
original = probe('before upgrade')
old_agent = hashlib.sha256(P('/usr/local/bin/ctlvps-agent').read_bytes()).hexdigest()
# Hold the old agent while the controller upgrades: retain its deployed node.
run('systemctl', 'stop', 'ctlvps-agent')
identifier = uuid.uuid4().hex
request('/api/v1/system/maintenance', {'id': identifier, 'role': 'controller', 'action': 'update',
    'version': version, 'confirm': 'VpsCT', 'password': password})
def controller_upgraded():
    for job in request('/api/v1/system/maintenance').get('jobs', []):
        if job['id'] == identifier and job['status'] == 'failed':
            log = P('/var/lib/ctlvps-maintenance') / identifier / 'worker.log'
            if log.exists(): print('Upgrade worker diagnostics:', log.read_text()[-5000:], flush=True)
            raise AssertionError('web controller upgrade failed: ' + str(job.get('message', job.get('stage'))))
    pid = run('systemctl', 'show', 'ctlvpsd', '-p', 'MainPID', '--value').stdout.strip()
    if not pid.isdigit() or int(pid) == 0: return False
    with P('/proc/' + pid + '/exe').open('rb') as executable:
        actual = hashlib.file_digest(executable, 'sha256').hexdigest()
    return actual == controller_sha and (root / 'current/VERSION').read_text().strip() == version and request('/healthz?ready=1')
wait(controller_upgraded, 'web controller upgrade')
probe('new controller with offline legacy agent', original)
assert hashlib.sha256(P('/usr/local/bin/ctlvps-agent').read_bytes()).hexdigest() == old_agent
run('systemctl', 'start', 'ctlvps-agent')
target_sha = hashlib.sha256(P(f'/assets/ctlvps-agent-linux-{arch}').read_bytes()).hexdigest()
wait(lambda: hashlib.sha256(P('/usr/local/bin/ctlvps-agent').read_bytes()).hexdigest() == target_sha, 'legacy agent auto update')
wait(lambda: applied(candidate=True), 'candidate node application')
probe('new controller and agent', original)
# Official latest advances while the installed controller remains on its pinned
# release. A same-version web reinstall must still verify that pinned release.
P('/tmp/newer-catalog').mkdir()
P('/tmp/newer-catalog/SHA256SUMS').write_text('0' * 64 + f'  ctlvps-agent-linux-{arch}\n')
latest = '/tmp/newer-catalog'
wait(lambda: not any(j['status'] in ('running', 'queued') for j in request(f'/api/v1/servers/{sid}/maintenance')['jobs']), 'previous maintenance completion')
identifier = uuid.uuid4().hex
request(f'/api/v1/servers/{sid}/maintenance', {'id': identifier, 'role': 'agent', 'action': 'update',
    'version': request(f'/api/v1/servers/{sid}/maintenance')['target_version'],
    'confirm': server['name'], 'password': password})
def reinstalled():
    return any(j['id'] == identifier and j['status'] == 'succeeded' for j in request(f'/api/v1/servers/{sid}/maintenance')['jobs'])
wait(reinstalled, 'pinned-version reinstall after upstream latest advances')
wait(lambda: applied(candidate=True), 'reinstalled agent application')
probe('pinned web reinstall with newer upstream latest', original)
assert not P('/etc/ctlvps/security.json').exists()
print('PASS actual historical web upgrade, offline compatibility, default checksum pinning and real DNS traffic', flush=True)
