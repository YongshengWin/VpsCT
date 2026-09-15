#!/usr/bin/env python3
"""Service lifecycle double, exclusively for disposable installer test containers."""
import os
import pathlib
import pwd
import signal
import subprocess
import sys
import time

pidfile = pathlib.Path('/run/ctlvps-test.pid')

def active():
    if not pidfile.exists():
        return False
    try:
        stat = pathlib.Path(f'/proc/{int(pidfile.read_text())}/stat').read_text()
        return stat.split(') ', 1)[1][0] != 'Z'
    except (ValueError, FileNotFoundError, ProcessLookupError):
        return False

args = sys.argv[1:]
args = [arg for arg in args if arg not in ('--system', '--quiet', '--no-reload')]
command = args[0]
if command == 'daemon-reload':
    sys.exit(0)
if any(a in ('caddy', 'caddy.service') for a in args):
    # The Caddy branch tests official package installation and config validation;
    # public ACME issuance and real systemd are checked on a deployment host.
    sys.exit(0)
if not any(a in ('ctlvpsd', 'ctlvpsd.service') for a in args):
    sys.exit('test double only supports ctlvpsd')
if command == 'is-active':
    sys.exit(0 if active() else 3)
if command == 'stop':
    if active():
        os.kill(int(pidfile.read_text()), signal.SIGTERM)
        for _ in range(100):
            if not active():
                break
            time.sleep(0.1)
        else:
            sys.exit('service did not stop')
    pidfile.unlink(missing_ok=True)
    sys.exit(0)
if command in ('start', 'enable'):
    if not active():
        env = os.environ.copy()
        for line in pathlib.Path('/etc/ctlvps/ctlvpsd.env').read_text().splitlines():
            if line and not line.startswith('#'):
                key, value = line.split('=', 1)
                env[key] = value
        # Prevent optional network data downloads from delaying installer tests.
        with open('/tmp/ctlvps-test-service.log', 'ab') as log:
            user = pwd.getpwnam('ctlvps')
            process = subprocess.Popen(['/opt/ctlvps/ctlvpsd'], env=env, stdout=log, user=user.pw_uid, group=user.pw_gid,
                                       stderr=subprocess.STDOUT, start_new_session=True)
        pidfile.write_text(str(process.pid))
    sys.exit(0)
sys.exit(f'unsupported test command: {command}')
