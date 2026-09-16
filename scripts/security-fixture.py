#!/usr/bin/env python3
"""Ephemeral signing authority, confined to disposable integration containers."""
import json, os, pathlib, platform, shutil, ssl, subprocess, sys
from http.server import ThreadingHTTPServer, SimpleHTTPRequestHandler
P=pathlib.Path
assert P('/.dockerenv').exists(), 'fixture authority is only for disposable containers'
root=P('/fixtures/security'); arch={'x86_64':'amd64','aarch64':'arm64'}[platform.machine()]
signer=f'/src/bin/ctlvps-sign-linux-{arch}'
def run(*args): subprocess.run(args,check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
if sys.argv[1]=='serve':
    os.chdir(root/'metadata')
    server=ThreadingHTTPServer(('127.0.0.1',9444),SimpleHTTPRequestHandler)
    ctx=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER);ctx.load_cert_chain(root/'tls.crt',root/'tls.key');server.socket=ctx.wrap_socket(server.socket,server_side=True);server.serve_forever()
elif sys.argv[1]=='init':
    root.mkdir(parents=True,exist_ok=True)
    run(signer,'--mode','init','--keys',str(root/'keys'))
    (root/'keys/security-policy.json').write_text(json.dumps({'schema':1,'product':'VpsCT','min_epoch':{},'revoked':[]}))
    run('openssl','req','-x509','-newkey','rsa:2048','-nodes','-days','2','-keyout',str(root/'tls.key'),'-out',str(root/'tls.crt'),'-subj','/CN=127.0.0.1','-addext','subjectAltName=IP:127.0.0.1')
    shutil.copyfile(root/'tls.crt','/usr/local/share/ca-certificates/ctlvps-fixture.crt');run('update-ca-certificates')
    P('/etc/ctlvps').mkdir(exist_ok=True)
    shutil.copyfile(root/'keys/root.json','/etc/ctlvps/root.json')
    P('/etc/ctlvps/security.json').write_text(json.dumps({'schema':1,'root_file':'/etc/ctlvps/root.json','metadata_url':'https://127.0.0.1:9444','private_metadata':True,'actions':['controller.update','controller.uninstall','controller.purge','agent.update','agent.uninstall','agent.purge','core.install','agent.configure']}))
    P('/usr/local/libexec').mkdir(exist_ok=True)
    shutil.copyfile(f'/src/bin/ctlvps-verify-linux-{arch}','/usr/local/libexec/ctlvps-verify');P('/usr/local/libexec/ctlvps-verify').chmod(0o755)
    (root/'metadata').mkdir();(root/'manifest.json').write_text('[]');(root/'generation').write_text('0')
    with (root/'server.log').open('w') as log:
        subprocess.Popen(['python3',__file__,'serve'],stdout=log,stderr=log,start_new_session=True)
elif sys.argv[1]=='sign':
    component,version,path=sys.argv[2:]; files=json.loads((root/'manifest.json').read_text())
    files=[f for f in files if f['path']!=path]
    files.append({'path':path,'identity':{'product':'VpsCT','component':component,'version':version,'arch':arch,'epoch':1}})
    (root/'manifest.json').write_text(json.dumps(files));gen=int((root/'generation').read_text())+1
    out=root/f'g{gen}';run(signer,'--keys',str(root/'keys'),'--manifest',str(root/'manifest.json'),'--out',str(out),'--metadata-version',str(gen))
    for f in out.iterdir():
        if f.is_dir():shutil.copytree(f,root/'metadata'/f.name,dirs_exist_ok=True)
        elif f.name!='timestamp.json':shutil.copyfile(f,root/'metadata'/f.name)
    shutil.copyfile(out/'timestamp.json',root/'metadata/timestamp.next');os.replace(root/'metadata/timestamp.next',root/'metadata/timestamp.json');(root/'generation').write_text(str(gen))
else: raise SystemExit('unknown mode')
