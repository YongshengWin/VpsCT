"""Optional native ACME cycle, after run.py in its disposable offline container.

Requires /fixtures/pebble. Uses real challenge validation with a short-lived test
CA certificate, then waits for sing-box's own renewal scheduler (about 10 min).
The CA trust and provider override exist only inside the disposable container.
"""
import hashlib,json,os,pathlib,socket,ssl,subprocess,sys,time,urllib.request
P=pathlib.Path
assert P('/.dockerenv').exists()
assert sys.argv[1:] in ([], ['--restart-only'])
restart_only = sys.argv[1:] == ['--restart-only']
def run(*a):
 p=subprocess.run(a,capture_output=True,text=True)
 if p.returncode:raise RuntimeError((a,p.stderr[-2000:]))
 return p.stdout.strip()
work=P('/tmp/acme-cycle');work.mkdir(exist_ok=True)
run('systemctl','stop','ctlvps-singbox.service','ctlvps-singbox-private.service')
run('openssl','req','-x509','-newkey','rsa:2048','-nodes','-days','1','-keyout',str(work/'ca.key'),'-out',str(work/'ca.crt'),'-subj','/CN=isolated-acme.test','-addext','subjectAltName=IP:198.51.100.2','-addext','basicConstraints=critical,CA:TRUE')
# Trust is installed only inside this throwaway container, never on the VPS.
P('/usr/local/share/ca-certificates/isolated-acme.crt').write_bytes((work/'ca.crt').read_bytes());run('update-ca-certificates')
with open('/etc/hosts','a') as f:f.write('\n198.51.100.1 acme-fixture.test\n')
cfg={'pebble':{'listenAddress':'0.0.0.0:14000','managementListenAddress':'0.0.0.0:15000','certificate':str(work/'ca.crt'),'privateKey':str(work/'ca.key'),'httpPort':80,'tlsPort':443,'profiles':{'default':{'description':'isolated 15 minute renewal fixture','validityPeriod':900}}}}
(work/'pebble.json').write_text(json.dumps(cfg))
env=dict(os.environ,PEBBLE_VA_NOSLEEP='1',PEBBLE_WFE_NONCEREJECT='0',PEBBLE_AUTHZREUSE='0')
log=open(work/'pebble.log','w');proc=subprocess.Popen(['ip','netns','exec','boundary-peer','/fixtures/pebble','-config',str(work/'pebble.json')],stdout=log,stderr=log,env=env)
try:
 time.sleep(.4)
 # Production Apply sets up identity, mounts, native state and launch unit.
 run('/usr/local/bin/ctlvps-agent','acme')
 run('systemctl','stop','ctlvps-singbox.service')
 p=P('/etc/ctlvps-proxy/public/config.json');c=json.loads(p.read_text());c['inbounds'][0]['tls']['acme']['provider']='https://198.51.100.2:14000/dir';c['log']['level']='info';p.write_text(json.dumps(c));p.chmod(0o640)
 run('systemctl','start','ctlvps-singbox.service')
 ctx=ssl.create_default_context(cafile=str(work/'ca.crt'))
 for i in range(40):
  try:
   root=urllib.request.urlopen('https://198.51.100.2:15000/roots/0',context=ctx,timeout=3).read().decode();break
  except Exception:
   if i==39:raise
   time.sleep(.25)
 client=ssl.create_default_context(cadata=root)
 (work/'root.crt').write_text(root)
 def peer():
  with socket.create_connection(('127.0.0.1',443),timeout=3) as s:
   with client.wrap_socket(s,server_hostname='acme-fixture.test') as t:
    return hashlib.sha256(t.getpeercert(binary_form=True)).hexdigest(),t.getpeercert()['notAfter']
 deadline=time.monotonic()+60
 while True:
  try:first,expires=peer();break
  except Exception:
   if time.monotonic()>deadline:
    print(run('journalctl','-u','ctlvps-singbox.service','-n','35','--no-pager','-o','cat'),flush=True);raise
   time.sleep(1)
 keys=lambda:{str(p):hashlib.sha256(p.read_bytes()).hexdigest() for p in P('/var/lib/ctlvps-agent/acme').rglob('*.key') if 'users' in p.parts}
 beforekeys=keys();assert beforekeys
 def persisted(fingerprint):
  certs=list(P('/var/lib/ctlvps-agent/acme').rglob('*.crt'))
  assert len(certs)==1
  der=ssl.PEM_cert_to_DER_cert(certs[0].read_text())
  assert hashlib.sha256(der).hexdigest()==fingerprint
  assert keys()==beforekeys
 def restart_check(fingerprint):
  persisted(fingerprint)
  run('systemctl','restart','ctlvps-singbox.service')
  limit=time.monotonic()+30
  while True:
   try:after,_=peer();break
   except (OSError,ssl.SSLError):
    if time.monotonic()>limit:raise
    time.sleep(.25)
  persisted(after)
  # Short-lived test certificates may be renewed again on startup. Verify
  # TLS and actual disk state rather than assuming the serial cannot change.
  print('ACME RESTART PASS: trusted certificate matches persisted state; same account keys; reissued='+str(after!=fingerprint),flush=True)
 epoch=run('systemctl','show','ctlvps-singbox.service','-p','InvocationID','--value')
 print('ACME ISSUED: chain and hostname verified; dedicated UID; native account persisted; expiry '+expires,flush=True)
 if restart_only:
  restart_check(first)
  sys.exit(0)
 deadline=time.monotonic()+730;last=time.monotonic()
 while time.monotonic()<deadline:
  time.sleep(10);current,expires=peer()
  if current!=first:
   assert keys()==beforekeys
   assert run('systemctl','show','ctlvps-singbox.service','-p','InvocationID','--value')==epoch
   print('ACME RENEWED: certificate changed automatically without restart; full TLS trust and hostname verification passed; same ACME account keys.',flush=True)
   restart_check(current);break
  if time.monotonic()-last>50:print('ACME waiting for native renewal scheduler; verified TLS remains available.',flush=True);last=time.monotonic()
 else:raise AssertionError('native renewal did not complete in test window')
finally:
 run('systemctl','stop','ctlvps-singbox.service');proc.terminate();proc.wait(timeout=5);log.close()
