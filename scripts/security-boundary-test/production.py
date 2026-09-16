"""Exercise the production Apply renderer and Go sandbox in an offline container."""
import hashlib,json,pathlib,shutil,socket,ssl,struct,subprocess,time
P=pathlib.Path

def verify_production(run):
    # Real installations keep management configuration under a private parent.
    # Proxy runtime config must work without relaxing that directory's mode.
    P('/etc/ctlvps').mkdir(exist_ok=True)
    P('/etc/ctlvps').chmod(0o750)
    launcher='/usr/local/bin/ctlvps-agent'
    shutil.copyfile('/fixtures/ctlvps-agent',launcher);P(launcher).chmod(0o755)
    def apply(mode=None,success=True):
        r=subprocess.run([launcher]+([mode] if mode else []),text=True,capture_output=True)
        if success and r.returncode:raise RuntimeError(r.stdout+r.stderr)
        if not success:assert r.returncode!=0
        return r.stdout
    def epoch():return run('systemctl','show','ctlvps-singbox.service','-p','InvocationID','--value')
    def connect(port=443,host='198.51.100.2'):
        deadline=time.monotonic()+5
        while True:
            try:
                tcp=socket.create_connection(('127.0.0.1',port),timeout=3);break
            except ConnectionRefusedError:
                if time.monotonic()>deadline:raise
                time.sleep(.05)
        ctx=ssl.create_default_context(cafile='/var/lib/ctlvps-agent/source.crt')
        with tcp:
            with ctx.wrap_socket(tcp,server_hostname='fixture.test') as tls:
                tls.sendall(hashlib.sha224(b'synthetic-password').hexdigest().encode()+b'\r\n\x01\x01'+socket.inet_aton(host)+struct.pack('!H',18081)+b'\r\nproduction-fixture')
                assert tls.recv(64)==b'production-fixture'
    assert 'changed true' in apply()
    from guard import verify_guard
    verify_guard(run,connect,epoch)
    connect();before=epoch()
    assert 'changed false' in apply();assert before==epoch()
    apply('bad',False);assert before==epoch();connect()
    with socket.socket() as s:
        s.bind(('0.0.0.0',21999));s.listen()
        apply('collision',False)
    assert run('systemctl','is-active','ctlvps-singbox.service')=='active';connect()
    # A content-addressed certificate snapshot changes the config and restarts.
    run('openssl','req','-x509','-newkey','rsa:2048','-nodes','-days','2','-keyout','/var/lib/ctlvps-agent/source.key','-out','/var/lib/ctlvps-agent/source.crt','-subj','/CN=fixture.test','-addext','subjectAltName=DNS:fixture.test')
    assert 'changed true' in apply();connect()
    assert 'changed true' in apply('mixed');connect();connect(444,'10.123.0.2')
    ids=[run('systemctl','show',u,'-p','User','--value') for u in ['ctlvps-singbox.service','ctlvps-singbox-private.service']]
    assert ids==['ctlvps-sb','ctlvps-sp']
    for user,other in [('ctlvps-sb','private'),('ctlvps-sp','public')]:
        r=subprocess.run(['runuser','-u',user,'--','cat','/etc/ctlvps-proxy/'+other+'/config.json'],capture_output=True)
        assert r.returncode!=0
    for unit in ['ctlvps-singbox.service','ctlvps-singbox-private.service']:
        pid=run('systemctl','show',unit,'-p','MainPID','--value')
        status=P('/proc/'+pid+'/status').read_text()
        assert 'CapEff:\t0000000000002400' in status and 'NoNewPrivs:\t1' in status
    print('PRODUCTION PASS: real Apply, nonroot Go sandbox, TLS forwarding, idempotence, invalid config preservation, activation rollback, cert replacement, mixed private groups and distinct UID isolation.')
    from protocols import verify_protocols
    verify_protocols(run,apply)
    run('systemctl','stop','ctlvps-singbox.service','ctlvps-singbox-private.service')
    run('systemctl','stop','ctlvps-proxy-guard.timer','ctlvps-proxy-guard.service')
    # After a reboot the volatile readiness marker is absent. No proxy may
    # open a listener before the root agent has installed egress rules.
    P('/run/ctlvps-proxy/egress-ready').unlink()
    run('systemctl','start','ctlvps-singbox.service')
    time.sleep(.2)
    assert run('systemctl','show','ctlvps-singbox.service','-p','MainPID','--value')=='0'
    run('systemctl','stop','ctlvps-singbox.service')
    if P('/fixtures/snell-server').exists():
        shutil.copyfile('/fixtures/snell-server','/opt/ctlvps/bin/snell-server');P('/opt/ctlvps/bin/snell-server').chmod(0o755)
        assert 'changed true' in apply('snell')
        pid=run('systemctl','show','ctlvps-snell@24443.service','-p','MainPID','--value')
        status=P('/proc/'+pid+'/status').read_text()
        assert 'CapEff:\t0000000000000400' in status and 'NoNewPrivs:\t1' in status
        with socket.create_connection(('127.0.0.1',24443),timeout=2):pass
        before=run('systemctl','show','ctlvps-snell@24443.service','-p','InvocationID','--value')
        assert 'changed false' in apply('snell')
        assert before==run('systemctl','show','ctlvps-snell@24443.service','-p','InvocationID','--value')
        from snell_client import verify_snell_client
        verify_snell_client()
        run('systemctl','stop','ctlvps-snell@24443.service')
        print('SNELL PASS: official 5.0.1 low-privilege startup, listening and idempotence.')
    print('BOOT GATE PASS: proxy refuses startup before current-boot egress readiness.')
    assert P('/etc/ctlvps').stat().st_mode & 0o777 == 0o750
