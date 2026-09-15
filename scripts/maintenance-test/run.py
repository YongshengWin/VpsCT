"""Destructive fixtures. Never run outside the runner's disposable container.

Uses the real API, root maintenance daemon, copied workers and systemd cgroups.
Release downloads are fixed local fixtures; no GitHub release is modified.
"""
import hashlib
import http.client
import json
import os
import pathlib
import platform
import re
import shutil
import socket
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
asset = next(P('/assets').glob(f'*-linux-{arch}.tar.gz'))
root = P('/opt/ctlvps')
base = 'http://127.0.0.1:8080'
password = 'maintenance-fixture-password'
cookie = ''

def run(*args, check=True):
    return subprocess.run(args, check=check, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)

def wait(check, timeout=70):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            result = check()
            if result:
                return result
        except (OSError, ValueError, urllib.error.URLError):
            pass
        time.sleep(.25)
    raise AssertionError('condition timed out')

def active(unit):
    return run('systemctl', 'is-active', '--quiet', unit, check=False).returncode == 0

def request(path, payload=None, expected=200, bearer=None):
    global cookie
    headers = {'Content-Type': 'application/json', 'Origin': base}
    if cookie:
        headers['Cookie'] = cookie
    if bearer:
        headers['Authorization'] = 'Bearer ' + bearer
    body = json.dumps(payload).encode() if payload is not None else None
    req = urllib.request.Request(base + path, data=body, headers=headers)
    try:
        response = urllib.request.urlopen(req, timeout=10)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        raw = response.read()
        assert response.status == expected, (path, response.status, raw)
        if response.headers.get('Set-Cookie'):
            cookie = response.headers['Set-Cookie'].split(';', 1)[0]
        return json.loads(raw) if raw else None

def root_status(job_id, expected):
    path = P('/var/lib/ctlvps-maintenance') / job_id / 'status.json'
    result = wait(lambda: path.exists() and json.loads(path.read_text()).get('status') not in ('queued', 'running') and json.loads(path.read_text()), 110)
    if result['status'] != expected:
        print((path.parent / 'worker.log').read_text())
    assert result['status'] == expected, result
    return result

def controller_job(action, version='', purge=False):
    job_id = uuid.uuid4().hex
    request('/api/v1/system/maintenance', {'id': job_id, 'role': 'controller', 'action': action,
            'version': version, 'purge': purge, 'confirm': 'VpsCT', 'password': password}, 202)
    return job_id

def fixture_release(version, broken=False, corrupt=False):
    package = P('/fixtures/package-' + version)
    shutil.copytree(root / 'current', package, symlinks=False)
    (package / 'VERSION').write_text(version + '\n')
    if broken:
        (package / 'ctlvpsd').write_text('#!/bin/sh\nif [ "${1:-}" = version ]; then echo fixture; exit 0; fi\necho dirty > /opt/ctlvps/data/failed-upgrade-marker\nexit 1\n')
        (package / 'ctlvpsd').chmod(0o755)
    out = P('/fixtures/releases') / version
    out.mkdir(parents=True)
    tar = out / f'ctlvps-{version}-linux-{arch}.tar.gz'
    with tarfile.open(tar, 'w:gz') as archive:
        for path in package.iterdir():
            archive.add(path, arcname=path.name)
    (out / 'SHA256SUMS').write_text(hashlib.sha256(tar.read_bytes()).hexdigest() + '  ' + tar.name + '\n')
    if corrupt:
        with tar.open('ab') as stream:
            stream.write(b'corrupt-fixture')

# Install the real binaries/services using the default layout.
run('useradd', '--system', '--user-group', '--home-dir', '/opt/ctlvps', '--no-create-home', '--shell', '/usr/sbin/nologin', 'ctlvps')
(root / 'releases/initial').mkdir(parents=True)
with tarfile.open(asset) as archive:
    archive.extractall(root / 'releases/initial', filter='data')
# Derive newer targets from the supplied release, including real release tags.
initial_version = (root / 'releases/initial/VERSION').read_text().strip()
match = re.fullmatch(r'v(\d+)\.(\d+)\.(\d+)(?:-[A-Za-z0-9.-]+)?', initial_version)
assert match, initial_version
major, minor, patch = map(int, match.groups())
update_version, broken_version, corrupt_version = (
    f'v{major}.{minor}.{patch + offset}-maintenance-test' for offset in (1, 2, 3)
)
for name, target in [('current', 'releases/initial'), ('ctlvpsd', 'current/ctlvpsd'), ('agents', 'current/agents')]:
    (root / name).symlink_to(target)
(root / 'REPOSITORY').write_text('example/VpsCT\n')
(root / 'current/REPOSITORY').write_text('example/VpsCT\n')
(root / 'data').mkdir()
run('chown', 'ctlvps:ctlvps', str(root / 'data'))
P('/etc/ctlvps').mkdir()
P('/etc/ctlvps/ctlvpsd.env').write_text('CTLVPS_SITE_URL=https://127.0.0.1:9443\nCTLVPS_LISTEN=127.0.0.1:8080\nCTLVPS_DATA_DIR=/opt/ctlvps/data\nCTLVPS_AGENT_BIN_DIR=/opt/ctlvps/agents\n')
for name in ['ctlvpsd', 'ctlvps-maintenance']:
    shutil.copyfile('/src/deploy/' + name + '.service', '/etc/systemd/system/' + name + '.service')
run('systemctl', 'daemon-reload')
run('systemctl', 'start', 'ctlvpsd', 'ctlvps-maintenance')
wait(lambda: request('/healthz'))
request('/api/v1/auth/setup', {'username': 'test-admin', 'password': password, 'setup_token': (root / 'data/setup-token').read_text().strip()})
assert request('/api/v1/system/maintenance')['available']
print('PASS real root helper available to unprivileged controller', flush=True)

# A second Unix user must not be able to invoke the root executor.
run('useradd', '--system', 'outsider')
denied = run('runuser', '-u', 'outsider', '--', 'curl', '--unix-socket', '/run/ctlvps-maintenance/control.sock', 'http://maintenance/status', check=False)
assert denied.returncode != 0
print('PASS Unix socket denies unrelated users', flush=True)

# Synthetic release transport; health requests still use the real curl.
P('/fixtures/releases').mkdir(parents=True)
P('/usr/local/bin/apt-get').write_text('#!/bin/sh\nexit 0\n')
P('/usr/local/bin/apt-get').chmod(0o755)
P('/usr/local/bin/curl').write_text('''#!/usr/bin/python3
import pathlib,shutil,sys,os
args=sys.argv[1:]
urls=[arg for arg in args if arg.startswith('https://github.com/example/VpsCT/releases/download/')]
if urls:
    tail=urls[0].split('/download/',1)[1]
    source=pathlib.Path('/fixtures/releases')/tail
    if source.is_file(): shutil.copyfile(source,args[args.index('-o')+1]);sys.exit(0)
    sys.exit(22)
os.execv('/usr/bin/curl',['/usr/bin/curl']+args)
''')
P('/usr/local/bin/curl').chmod(0o755)
fixture_release(update_version)
fixture_release(broken_version, broken=True)
fixture_release(corrupt_version, corrupt=True)

before = (root / 'current').resolve()
job = controller_job('update', update_version)
root_status(job, 'succeeded')
assert (root / 'current').resolve() != before
assert request('/api/v1/auth/setup')['needs_setup'] is False
assert list((root / 'backups').glob('*/data.tar.gz'))
assert active('ctlvpsd') and active('ctlvps-maintenance')
assert any(j['id'] == job and j['status'] == 'succeeded' for j in request('/api/v1/system/maintenance')['jobs'])
print('PASS web update survives API/helper restart and retains administrator', flush=True)

previous = (root / 'current').resolve()
job = controller_job('update', broken_version)
root_status(job, 'rolled_back')
assert (root / 'current').resolve() == previous
assert not (root / 'data/failed-upgrade-marker').exists()
assert request('/api/v1/auth/setup')['needs_setup'] is False
print('PASS failed startup restores previous binary AND previous data', flush=True)

pid = run('systemctl', 'show', 'ctlvpsd', '-p', 'MainPID', '--value').stdout
job = controller_job('update', corrupt_version)
root_status(job, 'failed')
assert run('systemctl', 'show', 'ctlvpsd', '-p', 'MainPID', '--value').stdout == pid
print('PASS checksum failure leaves running controller untouched', flush=True)

# HTTPS loopback bridge, with an isolated trusted test certificate, lets the
# real agent and detached worker report after their original service exits.
run('openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-keyout', '/tmp/test.key', '-out', '/usr/local/share/ca-certificates/maintenance-test.crt', '-days', '1', '-subj', '/CN=127.0.0.1', '-addext', 'subjectAltName=IP:127.0.0.1')
run('update-ca-certificates')
class Bridge(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass
    def do_GET(self):
        self.forward()
    def do_POST(self):
        self.forward()
    def forward(self):
        body = self.rfile.read(int(self.headers.get('Content-Length', '0')))
        connection = http.client.HTTPConnection('127.0.0.1', 8080, timeout=20)
        connection.request(self.command, self.path, body, dict(self.headers))
        response = connection.getresponse()
        data = response.read()
        self.send_response(response.status)
        self.send_header('Content-Length', str(len(data)))
        self.send_header('Content-Type', 'application/json')
        self.end_headers()
        self.wfile.write(data)
        connection.close()
bridge = ThreadingHTTPServer(('127.0.0.1', 9443), Bridge)
tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
tls.load_cert_chain('/usr/local/share/ca-certificates/maintenance-test.crt', '/tmp/test.key')
bridge.socket = tls.wrap_socket(bridge.socket, server_side=True)
threading.Thread(target=bridge.serve_forever, daemon=True).start()

server = request('/api/v1/servers', {'name': 'maintenance-fixture'}, 201)
sid = server['id']
enrol = request(f'/api/v1/servers/{sid}/enroll-token', {})
shutil.copyfile(root / f'agents/ctlvps-agent-linux-{arch}', '/usr/local/bin/ctlvps-agent')
P('/usr/local/bin/ctlvps-agent').chmod(0o755)
# Write enrolment state via API, keeping the real token exclusively in this
# disposable machine and out of process command lines/log output.
enrolled = request('/api/agent/v1/enroll', {'enroll_token': enrol['token'], 'version': 'fixture', 'arch': arch})
P('/var/lib/ctlvps-agent').mkdir(mode=0o700)
state = {'server_url': 'https://127.0.0.1:9443', 'agent_token': enrolled['agent_token'], 'server_id': sid, 'poll_interval_sec': 1}
P('/var/lib/ctlvps-agent/state.json').write_text(json.dumps(state))
P('/var/lib/ctlvps-agent/state.json').chmod(0o600)
P('/etc/systemd/system/ctlvps-agent.service').write_text('[Service]\nExecStart=/usr/local/bin/ctlvps-agent run --state /var/lib/ctlvps-agent\nRestart=always\nRestartSec=1\n[Install]\nWantedBy=multi-user.target\n')
run('systemctl', 'daemon-reload')
run('systemctl', 'start', 'ctlvps-agent')
wait(lambda: request(f'/api/v1/servers/{sid}/maintenance')['available'], 100)

def agent_job(action, purge=False):
    identifier = uuid.uuid4().hex
    status = request(f'/api/v1/servers/{sid}/maintenance')
    request(f'/api/v1/servers/{sid}/maintenance', {'id': identifier, 'role': 'agent', 'action': action,
            'version': status['target_version'] if action == 'update' else '', 'purge': purge,
            'password': password, 'confirm': 'maintenance-fixture'}, 202)
    return identifier

job = agent_job('update')
root_status(job, 'succeeded')
wait(lambda: any(j['id'] == job and j['status'] == 'succeeded' for j in request(f'/api/v1/servers/{sid}/maintenance')['jobs']))
assert active('ctlvps-agent') and active('ctlvpsd')
print('PASS real agent claims, restarts, verifies binary and reports success', flush=True)

distribution = root / f'agents/ctlvps-agent-linux-{arch}'
original_agent = distribution.read_bytes()
run('systemctl', 'stop', 'ctlvps-agent')
distribution.write_text('#!/bin/sh\nif [ "${1:-}" = version ]; then echo fixture; exit 0; fi\nexit 1\n')
job = agent_job('update')
run('systemctl', 'start', 'ctlvps-agent')
root_status(job, 'rolled_back')
wait(lambda: any(j['id'] == job and j['status'] == 'rolled_back' for j in request(f'/api/v1/servers/{sid}/maintenance')['jobs']))
assert P('/usr/local/bin/ctlvps-agent').read_bytes() == original_agent
assert active('ctlvps-agent') and active('ctlvpsd')
distribution.write_bytes(original_agent)
print('PASS failed agent startup restores the original running binary', flush=True)

job = agent_job('uninstall', purge=True)
root_status(job, 'succeeded')
wait(lambda: any(j['id'] == job and j['status'] == 'succeeded' for j in request(f'/api/v1/servers/{sid}/maintenance')['jobs']))
assert not P('/var/lib/ctlvps-agent').exists()
assert not P('/usr/local/bin/ctlvps-agent').exists()
assert active('ctlvpsd') and active('ctlvps-maintenance')
print('PASS agent purge reports from detached worker and preserves controller', flush=True)

# The control plane removes itself and its helper, but the copied worker lives
# in a different cgroup and writes a verifiable completion result.
job = controller_job('uninstall', purge=True)
root_status(job, 'succeeded')
assert not active('ctlvpsd') and not active('ctlvps-maintenance')
assert not (root / 'data').exists() and not (root / 'releases').exists()
wait(lambda: not (P('/var/lib/ctlvps-maintenance') / job / 'request.json').exists())
print('PASS controller purge completes after API and root helper exit', flush=True)
print('Maintenance integration: 9 scenarios passed', flush=True)
