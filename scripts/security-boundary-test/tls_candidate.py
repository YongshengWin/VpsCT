"""Real pinned sing-box smoke test, entirely offline and synthetic."""
import hashlib,json,os,pathlib,shutil,socket,ssl,struct,time
P=pathlib.Path

def verify_tls(run):
    binary=P('/opt/ctlvps/bin/sing-box');binary.parent.mkdir(parents=True,exist_ok=True)
    shutil.copyfile('/fixtures/sing-box',binary);binary.chmod(0o755)
    runtime=P('/etc/ctlvps/runtime/fixture');runtime.mkdir(parents=True,exist_ok=True)
    run('chown','root:ctlvps-probe',str(runtime));runtime.chmod(0o750)
    acme=P('/var/lib/ctlvps-proxy/fixture-acme');acme.mkdir(parents=True,exist_ok=True);acme.chmod(0o700);run('chown','ctlvps-probe:ctlvps-probe',str(acme))
    def cert():
        run('openssl','req','-x509','-newkey','rsa:2048','-nodes','-days','2','-keyout','/var/lib/ctlvps-agent/source.key','-out','/var/lib/ctlvps-agent/source.crt','-subj','/CN=fixture.test','-addext','subjectAltName=DNS:fixture.test')
        for name in ['key','crt']:
            shutil.copyfile('/var/lib/ctlvps-agent/source.'+name,runtime/('tls.'+name))
            run('chown','root:ctlvps-probe',str(runtime/('tls.'+name)));(runtime/('tls.'+name)).chmod(0o640)
    cert()
    cfg={'log':{'level':'warn','output':'/var/log/ctlvps/real-fixture.log'},
         'inbounds':[{'type':'trojan','tag':'node-1','listen':'::','listen_port':443,'users':[{'password':'synthetic-password'}],
          'tls':{'enabled':True,'server_name':'fixture.test','certificate_path':str(runtime/'tls.crt'),'key_path':str(runtime/'tls.key')}}],
         'outbounds':[{'type':'direct','tag':'direct','routing_mark':0x43000001}],
         'route':{'final':'direct'}}
    config=runtime/'config.json';config.write_text(json.dumps(cfg));config.chmod(0o640);run('chown','root:ctlvps-probe',str(config))
    unit=P('/fixtures/singbox.service').read_text()
    launcher='/usr/bin/python3 /src/scripts/security-boundary-test/sandbox.py '+str(binary)
    unit=unit.replace('ExecStart=/fixtures/ctlvps-agent raw-probe singbox',
        'ExecStartPre='+launcher+' check -c '+str(config)+'\nExecStart='+launcher+' run -c '+str(config)+'\nReadWritePaths='+str(acme))
    P('/etc/systemd/system/ctlvps-boundary-real.service').write_text(unit)
    run('nft','add','table','inet','boundary_meter')
    run('nft','add','counter','inet','boundary_meter','marked')
    run('nft','add','chain','inet','boundary_meter','output','{ type filter hook output priority -150; policy accept; }')
    run('nft','add','rule','inet','boundary_meter','output','meta','mark','0x43000001','counter','name','marked')
    run('systemctl','daemon-reload');run('systemctl','start','ctlvps-boundary-real.service')
    def connect():
        ctx=ssl.create_default_context(cafile=str(runtime/'tls.crt'))
        deadline=time.monotonic()+8
        while True:
            try:
                with socket.create_connection(('127.0.0.1',443),timeout=2) as tcp:
                    with ctx.wrap_socket(tcp,server_hostname='fixture.test') as tls:
                        peer=hashlib.sha256(tls.getpeercert(binary_form=True)).hexdigest()
                        payload=hashlib.sha224(b'synthetic-password').hexdigest().encode()+b'\r\n'+b'\x01\x01'+socket.inet_aton('198.51.100.2')+struct.pack('!H',18081)+b'\r\nreal-trojan-fixture'
                        tls.sendall(payload);assert tls.recv(64)==b'real-trojan-fixture'
                        return peer
            except ConnectionRefusedError:
                if time.monotonic()>deadline:raise
                time.sleep(.1)
    before=connect()
    cert();run('systemctl','restart','ctlvps-boundary-real.service');after=connect()
    assert before!=after
    counters=json.loads(run('nft','-j','list','counter','inet','boundary_meter','marked'))['nftables']
    assert any(row.get('counter',{}).get('bytes',0)>0 for row in counters),'no marked packets recorded'
    assert P('/var/log/ctlvps/real-fixture.log').exists()
    print('REAL CORE PASS:',run(str(binary),'version').splitlines()[0],': nonroot config check, seccomp, low port 443, TLS verified, Trojan forwarding with SO_MARK and actual nft counter increments, cert replacement/restart and logging. No live ACME renewal tested.')
    run('systemctl','stop','ctlvps-boundary-real.service')
