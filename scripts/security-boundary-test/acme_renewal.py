"""Official-core ACME regression in an offline, disposable systemd container.

The quick matrix and the native renewal scheduler run independently in CI.
Only this test container trusts the generated CA or uses its ACME directory.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import socket
import ssl
import struct
import subprocess
import time
import urllib.request

P = Path
UNIT = 'ctlvps-singbox.service'
CONFIG = P('/etc/ctlvps-proxy/public/config.json')


def run(*args, check=True):
    p = subprocess.run(args, capture_output=True, text=True, timeout=90)
    if check and p.returncode:
        raise RuntimeError((args, p.stdout[-1500:], p.stderr[-2500:]))
    return p.stdout.strip()


def wait_for(action, seconds=60):
    deadline = time.monotonic() + seconds
    while True:
        try:
            return action()
        except (OSError, ssl.SSLError, AssertionError):
            if time.monotonic() >= deadline:
                raise
            time.sleep(.25)


def setup_network():
    # Full boundary runs already created this namespace; standalone CI has none.
    if 'boundary-peer' in run('ip', 'netns', 'list'):
        return
    run('ip', 'link', 'set', 'lo', 'up')
    run('ip', 'netns', 'add', 'boundary-peer')
    run('ip', 'link', 'add', 'boundary-host', 'type', 'veth', 'peer', 'name', 'boundary-peer0')
    run('ip', 'link', 'set', 'boundary-peer0', 'netns', 'boundary-peer')
    run('ip', 'addr', 'add', '198.51.100.1/24', 'dev', 'boundary-host')
    run('ip', 'link', 'set', 'boundary-host', 'up')
    run('ip', 'netns', 'exec', 'boundary-peer', 'ip', 'addr', 'add', '198.51.100.2/24', 'dev', 'boundary-peer0')
    for name in ('boundary-peer0', 'lo'):
        run('ip', 'netns', 'exec', 'boundary-peer', 'ip', 'link', 'set', name, 'up')


def account_keys():
    return {str(p): hashlib.sha256(p.read_bytes()).hexdigest()
            for p in P('/var/lib/ctlvps-agent/acme').rglob('*.key') if 'users' in p.parts}


def fingerprint(cert):
    return hashlib.sha256(cert).hexdigest()


def epoch():
    return run('systemctl', 'show', UNIT, '-p', 'InvocationID', '--value')


def verify_egress():
    """Check real ACME permissions without relaxing destination isolation."""
    entries = json.loads(run('nft', '-j', 'list', 'table', 'inet', 'ctlvps_egress'))['nftables']
    rules = [entry['rule']['expr'] for entry in entries if 'rule' in entry
             and entry['rule']['chain'] == 'output'
             and any(expr.get('match', {}).get('right') ==
                     'ctlvps.slice/ctlvps-proxy.slice/ctlvps-proxy-public.slice'
                     for expr in entry['rule']['expr'])]
    dropped = [expr for expr in rules if {'drop': None} in expr]
    marks = [part['match']['right'] for expr in dropped for part in expr
             if part.get('match', {}).get('left') == {'meta': {'key': 'mark'}}
             and part['match']['op'] == '!=']
    assert marks == [{'set': [0, 0x43000001]}], ('ACME unmarked egress missing', marks)
    for family, prefix in [('ip', {'addr': '10.0.0.0', 'len': 8}),
                           ('ip6', {'addr': 'fc00::', 'len': 7})]:
        assert any(part.get('match', {}).get('left') ==
                   {'payload': {'protocol': family, 'field': 'daddr'}}
                   and {'prefix': prefix} in part['match']['right'].get('set', [])
                   for expr in dropped for part in expr
                   if isinstance(part.get('match', {}).get('right'), dict)), \
            ('private destination guard missing', family)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--scenario', choices=['all', 'handshake', 'renewal'], default='all')
    parser.add_argument('--restart-only', action='store_true', help='Legacy quick entry point; now covers the full ALPN matrix')
    args = parser.parse_args()
    assert P('/.dockerenv').exists() and P('/proc/1/comm').read_text().strip() == 'systemd'
    if args.restart_only:
        args.scenario = 'handshake'
    if args.scenario == 'all':
        # Independent CA lifetimes keep restart reuse separate from renewal.
        for scenario in ('handshake', 'renewal'):
            subprocess.run(['python3', __file__, '--scenario', scenario], check=True)
        return
    work = P('/tmp/acme-cycle')
    work.mkdir(exist_ok=True)
    setup_network()
    for source, target in [('/fixtures/ctlvps-agent', '/usr/local/bin/ctlvps-agent'), ('/fixtures/sing-box', '/opt/ctlvps/bin/sing-box')]:
        P(target).parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(source, target)
        P(target).chmod(0o755)
    P('/etc/ctlvps').mkdir(exist_ok=True)
    P('/etc/ctlvps').chmod(0o750)
    run('systemctl', 'stop', UNIT, 'ctlvps-singbox-private.service', check=False)
    state = P('/var/lib/ctlvps-agent/acme')
    if state.exists():
        # This is solely a disposable fixture; a new Pebble process has a new
        # in-memory account database and must not inherit another test CA's state.
        for child in state.iterdir():
            if child.is_dir():
                shutil.rmtree(child)
            else:
                child.unlink()
    run('openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1',
        '-keyout', str(work/'ca.key'), '-out', str(work/'ca.crt'), '-subj', '/CN=isolated-acme.test',
        '-addext', 'subjectAltName=IP:198.51.100.2,DNS:acme-v02.api.letsencrypt.org,DNS:acme-staging-v02.api.letsencrypt.org', '-addext', 'basicConstraints=critical,CA:TRUE')
    P('/usr/local/share/ca-certificates/isolated-acme.crt').write_bytes((work/'ca.crt').read_bytes())
    run('update-ca-certificates')
    cases = [('anytls', 'legacy'), ('trojan', 'legacy'), ('anytls', 'modern'), ('trojan', 'modern')]
    with open('/etc/hosts', 'a') as hosts:
        hosts.write('\n198.51.100.1 ' + ' '.join('acme-'+p+'-'+s+'.test' for p, s in cases) + '\n198.51.100.2 acme-v02.api.letsencrypt.org acme-staging-v02.api.letsencrypt.org\n')
    config = {'pebble': {'listenAddress': '0.0.0.0:14000', 'managementListenAddress': '0.0.0.0:15000',
              'certificate': str(work/'ca.crt'), 'privateKey': str(work/'ca.key'), 'httpPort': 80, 'tlsPort': 443,
              'profiles': {'default': {'description': 'isolated ACME fixture', 'validityPeriod': 900 if args.scenario == 'renewal' else 86400}}}}
    (work/'pebble.json').write_text(json.dumps(config))
    environment = dict(os.environ, PEBBLE_VA_NOSLEEP='1', PEBBLE_WFE_NONCEREJECT='0', PEBBLE_AUTHZREUSE='0')
    # Never inherit Pebble's challenge-bypass switch from the caller.
    environment.pop('PEBBLE_VA_ALWAYS_VALID', None)
    ca_log = open(work/'pebble.log', 'w')
    ca = subprocess.Popen(['ip', 'netns', 'exec', 'boundary-peer', '/fixtures/pebble', '-config', str(work/'pebble.json')], stdout=ca_log, stderr=ca_log, env=environment)
    directory = subprocess.Popen(['ip', 'netns', 'exec', 'boundary-peer', 'python3', str(P(__file__).with_name('acme_directory.py')), str(work)], stdout=ca_log, stderr=ca_log)
    # The CA namespace has no default route. HTTP-01 is blocked only in this
    # namespace, so actual issuance must complete via TLS-ALPN-01 on port 443.
    run('ip', 'netns', 'exec', 'boundary-peer', 'nft', 'delete', 'table', 'inet', 'acme_fixture', check=False)
    run('ip', 'netns', 'exec', 'boundary-peer', 'nft', 'add', 'table', 'inet', 'acme_fixture')
    run('ip', 'netns', 'exec', 'boundary-peer', 'nft', 'add', 'chain', 'inet', 'acme_fixture', 'output', '{ type filter hook output priority 0; policy accept; }')
    run('ip', 'netns', 'exec', 'boundary-peer', 'nft', 'add', 'rule', 'inet', 'acme_fixture', 'output', 'ip', 'daddr', '198.51.100.1', 'tcp', 'dport', '80', 'reject', 'with', 'tcp', 'reset')
    echo_code = '''import socket
s=socket.socket();s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1);s.bind(('0.0.0.0',18081));s.listen()
while True:
 c,_=s.accept()
 with c:
  while True:
   data=c.recv(128)
   if not data:break
   c.sendall(data)
'''
    echo = subprocess.Popen(['ip', 'netns', 'exec', 'boundary-peer', 'python3', '-c', echo_code])
    try:
        ca_context = ssl.create_default_context(cafile=str(work/'ca.crt'))
        def root_cert():
            return urllib.request.urlopen('https://198.51.100.2:15000/roots/0', context=ca_context, timeout=3).read().decode()
        root = wait_for(root_cert)
        def ready_directory():
            with urllib.request.urlopen('https://acme-v02.api.letsencrypt.org/directory', context=ca_context, timeout=3) as response:
                assert 'newOrder' in json.load(response)
        wait_for(ready_directory)
        (work/'root.crt').write_text(root)
        if args.scenario == 'renewal':
            cases = [('trojan', 'modern')]
        for protocol, schema in cases:
            domain = 'acme-'+protocol+'-'+schema+'.test'
            renewal_case = args.scenario == 'renewal'
            # Apply uses the production renderer, identity, sandbox, mount and
            # service transaction unchanged. Container-only DNS/directory routing
            # reaches Pebble; the CA namespace blocks HTTP-01 on port 80.
            result = run('/usr/local/bin/ctlvps-agent', 'acme', protocol, schema)
            assert 'changed true' in result
            verify_egress()
            generated = json.loads(CONFIG.read_text())
            password = generated['inbounds'][0]['users'][0]['password']
            tls = generated['inbounds'][0]['tls']
            assert tls['alpn'] == ['h2', 'http/1.1']
            assert ('certificate_provider' in tls) == (schema == 'modern')
            def peer(alpn=None):
                context = ssl.create_default_context(cadata=root)
                if alpn is not None:
                    context.set_alpn_protocols(alpn)
                with socket.create_connection(('127.0.0.1', 443), timeout=3) as tcp:
                    with context.wrap_socket(tcp, server_hostname=domain) as connection:
                        negotiated = connection.selected_alpn_protocol()
                        assert negotiated == (alpn[0] if alpn else None), (alpn, negotiated)
                        return fingerprint(connection.getpeercert(binary_form=True)), connection.getpeercert()['notAfter']
            first, expires = wait_for(peer)
            lifetime = ssl.cert_time_to_seconds(expires) - time.time()
            assert 0 < lifetime <= 1200 if renewal_case else lifetime > 43200, ('unexpected CA lifetime', lifetime)
            if schema == 'modern' and not renewal_case:
                # A new agent process must replace an old on-disk rendering,
                # restart its shared instance once, and then remain idempotent.
                old = json.loads(CONFIG.read_text())
                old['inbounds'][0]['tls'].pop('alpn')
                CONFIG.write_text(json.dumps(old))
                before = epoch()
                assert 'changed true' in run('/usr/local/bin/ctlvps-agent', 'acme', protocol, schema)
                assert epoch() != before
                before = epoch()
                assert 'changed false' in run('/usr/local/bin/ctlvps-agent', 'acme', protocol, schema)
                assert epoch() == before
                saved = CONFIG.read_bytes()
                invalid = subprocess.run(['/usr/local/bin/ctlvps-agent', 'acme-invalid', protocol, schema], capture_output=True, timeout=90)
                assert invalid.returncode != 0
                assert CONFIG.read_bytes() == saved and epoch() == before
                # The fixture stages candidate egress before Apply, just like
                # the coordinator. Return to the original desired state after
                # the rejected external-certificate candidate, so its narrower
                # permissions do not leak into later ACME renewal assertions.
                assert 'changed false' in run('/usr/local/bin/ctlvps-agent', 'acme', protocol, schema)
                assert CONFIG.read_bytes() == saved and epoch() == before
                verify_egress()
                print('ACME APPLY PASS:', protocol, 'old JSON replaced; shared instance restarted once; repeated Apply idempotent; invalid candidate preserved configuration; original ACME egress restored without restart.', flush=True)
            for alpn in (['h2', 'http/1.1'], ['http/1.1'], None):
                wait_for(lambda: peer(alpn))
                verify_proxy(work, protocol, domain, alpn, password)
            # An unrelated ALPN must continue to fail, with certificate checking on.
            try:
                peer(['unrelated-fixture-protocol'])
                raise AssertionError('unrelated ALPN was accepted')
            except ssl.SSLError:
                pass
            before_keys = account_keys()
            assert before_keys
            def persisted(expected):
                certs = [p for p in P('/var/lib/ctlvps-agent/acme').rglob('*.crt') if domain in str(p)]
                assert len(certs) == 1, certs
                assert fingerprint(ssl.PEM_cert_to_DER_cert(certs[0].read_text())) == expected
                assert account_keys() == before_keys
            persisted(first)
            print('ACME ALPN PASS:', protocol, schema, 'TLS-ALPN-01 issuance; verified chain/domain; h2/http1, http1 and absent ALPN; actual authenticated proxy traffic; unrelated ALPN rejected; expiry', expires, flush=True)
            if renewal_case:
                start_epoch = epoch()
                deadline = time.monotonic() + 730
                last = time.monotonic()
                while time.monotonic() < deadline:
                    time.sleep(10)
                    verify_egress()
                    current, _ = peer(['h2', 'http/1.1'])
                    if current != first:
                        assert epoch() == start_epoch
                        persisted(current)
                        verify_proxy(work, protocol, domain, ['h2', 'http/1.1'], password)
                        first = current
                        print('ACME RENEWED: native scheduler, no service restart, same account, persisted certificate and verified ALPN proxy traffic.', flush=True)
                        break
                    if time.monotonic() - last > 50:
                        print('ACME waiting for native renewal; verified ALPN TLS remains available.', flush=True)
                        last = time.monotonic()
                else:
                    raise AssertionError('native renewal did not complete in test window')
            run('systemctl', 'restart', UNIT)
            after, _ = wait_for(lambda: peer(['h2', 'http/1.1']))
            persisted(after)
            if not renewal_case:
                assert after == first, 'restart replaced a still-fresh certificate instead of reusing persisted state'
            verify_proxy(work, protocol, domain, None, password)
            print('ACME RESTART PASS:', protocol, schema, 'persistent account; fresh certificate reused' if not renewal_case else 'short-lived certificate matches persisted state; reissued='+str(after != first), flush=True)
            run('systemctl', 'stop', UNIT)
    except Exception:
        print(run('journalctl', '-u', UNIT, '-n', '60', '--no-pager', '-o', 'cat', check=False), flush=True)
        for log in P('/var/log/ctlvps').glob('sing-box*.log'):
            print(log.name, log.read_text()[-7000:], flush=True)
        print((work/'pebble.log').read_text()[-4000:], flush=True)
        raise
    finally:
        run('systemctl', 'stop', UNIT, check=False)
        for process in (echo, directory, ca):
            process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
        ca_log.close()


def verify_proxy(work, protocol, domain, alpn, password):
    tls = {'enabled': True, 'server_name': domain, 'certificate_path': str(work/'root.crt')}
    if alpn is not None:
        tls['alpn'] = alpn
    cfg = {'log': {'level': 'error'},
           'inbounds': [{'type': 'socks', 'listen': '127.0.0.1', 'listen_port': 24005}],
           'outbounds': [{'type': protocol, 'server': '127.0.0.1', 'server_port': 443,
                          'password': password, 'tls': tls}]}
    client = work/'client.json'
    client.write_text(json.dumps(cfg))
    with open(work/'client.log', 'w') as log:
        process = subprocess.Popen(['/opt/ctlvps/bin/sing-box', 'run', '-c', str(client)], stdout=log, stderr=log)
        try:
            def traffic():
                assert process.poll() is None, (work/'client.log').read_text()
                with socket.create_connection(('127.0.0.1', 24005), timeout=3) as sock:
                    sock.sendall(b'\x05\x01\x00')
                    assert recv_exact(sock, 2) == b'\x05\x00'
                    sock.sendall(b'\x05\x01\x00\x01'+socket.inet_aton('198.51.100.2')+struct.pack('!H', 18081))
                    reply = recv_exact(sock, 4)
                    assert reply[:3] == b'\x05\x00\x00', reply
                    lengths = {1: 4, 4: 16}
                    size = recv_exact(sock, 1)[0] if reply[3] == 3 else lengths[reply[3]]
                    recv_exact(sock, size + 2)
                    sock.sendall(b'acme-alpn-fixture')
                    assert recv_exact(sock, len(b'acme-alpn-fixture')) == b'acme-alpn-fixture'
            wait_for(traffic, seconds=15)
        finally:
            process.terminate()
            try:
                process.wait(timeout=3)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()


def recv_exact(sock, size):
    data = bytearray()
    while len(data) < size:
        part = sock.recv(size - len(data))
        if not part:
            raise ConnectionError('unexpected end of SOCKS/echo stream')
        data.extend(part)
    return bytes(data)


if __name__ == '__main__':
    main()
