"""Explicitly authorized native acceptance fixture; refuses existing agent state."""
import hashlib,ipaddress,json,os,pathlib,pwd,shutil,socket,ssl,struct,subprocess,sys,time
P=pathlib.Path
BASE=P(__file__).parent
MARK=P('/run/ctlvps-native-test')
DIRS=['/etc/ctlvps-proxy','/var/lib/ctlvps-agent','/var/lib/ctlvps-proxy','/run/ctlvps-proxy','/var/log/ctlvps','/opt/ctlvps/bin']
FILES=['/usr/local/bin/ctlvps-agent']
UNITS=['ctlvps-proxy-guard.timer','ctlvps-proxy-guard.service','ctlvps-singbox.service','ctlvps-singbox-private.service','ctlvps-proxy-check-public.service','ctlvps-proxy-check-private.service','ctlvps-snell@24443.service','ctlvps-proxy-public.slice','ctlvps-proxy-private.slice','ctlvps-proxy-n20.slice','ctlvps-proxy.slice']
USERS=['ctlvps-sb','ctlvps-sp','ctlvps-sn24443']
PORTS=[21999,24443,24543,24544,24005]
def run(*args,check=True,**kw):
 r=subprocess.run(args,capture_output=True,text=True,**kw)
 if check and r.returncode:raise RuntimeError((args,r.stderr[-2000:]))
 return r.stdout.strip()
def epoch():return run('systemctl','show','ctlvpsd.service','-p','InvocationID','--value')
def cleanup():
 if not (MARK/'authorized').exists():return
 for u in UNITS:run('systemctl','disable','--now',u,check=False)
 for u in UNITS:
  P('/etc/systemd/system',u).unlink(missing_ok=True)
  shutil.rmtree(P('/etc/systemd/system',u+'.d'),ignore_errors=True)
 run('systemctl','daemon-reload')
 for u in UNITS:run('systemctl','reset-failed',u,check=False)
 run('nft','delete','table','inet','ctlvps_egress',check=False)
 run('nft','delete','table','inet','ctlvps_native_test',check=False)
 run('ip','-6','rule','del','priority','8999','to','2001:db8:123::/64','lookup','main',check=False)
 run('ip','link','del','ct-native0',check=False)
 run('ip','netns','del','ctlvps-native-peer',check=False)
 for d in DIRS:shutil.rmtree(d,ignore_errors=True)
 for f in FILES:P(f).unlink(missing_ok=True)
 for u in USERS:
  run('userdel',u,check=False)
  run('groupdel',u,check=False)
 shutil.rmtree(MARK)
 print('CLEANUP PASS: native fixture services, files, accounts and network removed.',flush=True)
if sys.argv[1:]==['--cleanup']:
 cleanup();sys.exit(0)
assert sys.argv[1:]==['--execute'] and os.geteuid()==0 and not P('/.dockerenv').exists()
assert P('/proc/1/comm').read_text().strip()=='systemd'
for p in DIRS+FILES+[str(MARK)]:assert not os.path.lexists(p),('existing path',p)
for u in UNITS:
 assert run('systemctl','show',u,'-p','FragmentPath','--value')=='',('existing unit file',u)
 assert run('systemctl','show',u,'-p','ActiveState','--value')=='inactive',('active unit',u)
for u in USERS:
 try:pwd.getpwnam(u);raise AssertionError('existing test identity')
 except KeyError:pass
for table in ['ctlvps_egress','ctlvps_native_test']:
 r=subprocess.run(['nft','list','table','inet',table],capture_output=True)
 assert r.returncode!=0, 'existing nft table'
run('nft','list','tables')
for port in PORTS:
 for kind in [socket.SOCK_STREAM,socket.SOCK_DGRAM]:
  with socket.socket(socket.AF_INET6,kind) as s:
   if kind==socket.SOCK_STREAM:s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1)
   s.bind(('::',port))
networks=[ipaddress.ip_network(x) for x in ['198.51.100.0/24','10.123.0.0/24','2001:db8:123::/64']]
for fam in ['-4','-6']:
 for r in json.loads(run('ip',fam,'-j','route','show','table','all')):
  dest=r.get('dst','default')
  if dest=='default':continue
  n=ipaddress.ip_network(dest,strict=False)
  assert all(n.version!=x.version or not n.overlaps(x) for x in networks),'test route overlap'
assert 'ctlvps-native-peer' not in run('ip','netns','list')
assert 'ct-native0' not in [x['ifname'] for x in json.loads(run('ip','-j','link'))]
assert all(r.get('priority')!=8999 for r in json.loads(run('ip','-6','-j','rule'))),'test rule priority occupied'
original_epoch=epoch()
assert run('systemctl','is-active','ctlvpsd.service')=='active'
(BASE/'baseline.json').write_text(json.dumps({'controller_epoch':original_epoch}))
MARK.mkdir(mode=0o700);(MARK/'authorized').touch(mode=0o600)
processes=[]
try:
 ports=', '.join(map(str,PORTS))
 rules='add table inet ctlvps_native_test\nadd chain inet ctlvps_native_test input { type filter hook input priority -200; policy accept; }\n'
 rules+='add rule inet ctlvps_native_test input iifname != "lo" meta l4proto { tcp, udp } th dport { '+ports+' } drop\n'
 run('nft','-f','-',input=rules)
 for d in ['/opt/ctlvps/bin','/var/log/ctlvps','/var/lib/ctlvps-agent']:P(d).mkdir(mode=0o700,parents=True)
 for src,dst in [('ctlvps-agent','/usr/local/bin/ctlvps-agent'),('sing-box','/opt/ctlvps/bin/sing-box'),('snell-server','/opt/ctlvps/bin/snell-server')]:
  shutil.copyfile(BASE/src,dst);P(dst).chmod(0o755)
 P('/opt/ctlvps/bin').chmod(0o755);P('/var/log/ctlvps').chmod(0o755)
 # A synthetic endpoint has no default route or public listeners.
 run('ip','netns','add','ctlvps-native-peer')
 run('ip','link','add','ct-native0','type','veth','peer','name','ct-native1')
 run('ip','link','set','ct-native1','netns','ctlvps-native-peer')
 for address in ['198.51.100.1/24','10.123.0.1/24','2001:db8:123::1/64']:
  run('ip','addr','add',address,'dev','ct-native0',*(['nodad'] if ':' in address else []))
 run('ip','link','set','ct-native0','up')
 for address in ['198.51.100.2/24','10.123.0.2/24','2001:db8:123::2/64']:
  run('ip','netns','exec','ctlvps-native-peer','ip','addr','add',address,'dev','ct-native1',*(['nodad'] if ':' in address else []))
 run('ip','netns','exec','ctlvps-native-peer','ip','link','set','ct-native1','up')
 run('ip','netns','exec','ctlvps-native-peer','ip','link','set','lo','up')
 # Permit only our synthetic destination ahead of an existing IPv6 deny policy.
 run('ip','-6','rule','add','priority','8999','to','2001:db8:123::/64','lookup','main')
 echo='''import socket,threading,time
for family,addr in [(socket.AF_INET,'0.0.0.0'),(socket.AF_INET6,'::')]:
 def tcp(f=family,a=addr):
  s=socket.socket(f);s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1)
  if f==socket.AF_INET6:s.setsockopt(socket.IPPROTO_IPV6,socket.IPV6_V6ONLY,1)
  s.bind((a,18081));s.listen()
  while True:
   c,_=s.accept()
   with c:c.sendall(c.recv(128))
 def udp(f=family,a=addr):
  s=socket.socket(f,socket.SOCK_DGRAM)
  if f==socket.AF_INET6:s.setsockopt(socket.IPPROTO_IPV6,socket.IPV6_V6ONLY,1)
  s.bind((a,18081))
  while True:
   b,peer=s.recvfrom(128);s.sendto(b,peer)
 threading.Thread(target=tcp,daemon=True).start();threading.Thread(target=udp,daemon=True).start()
while True:time.sleep(1)
'''
 processes.append(subprocess.Popen(['ip','netns','exec','ctlvps-native-peer','python3','-c',echo]))
 env=dict(os.environ,CTLVPS_NATIVE_FIXTURE='1')
 def apply(mode=None,success=True):
  r=subprocess.run([str(BASE/'apply')]+([mode] if mode else []),env=env,capture_output=True,text=True)
  if success and r.returncode:raise RuntimeError(r.stderr[-3000:])
  if not success:assert r.returncode!=0
  return r.stdout
 def connect(port=24543,host='198.51.100.2'):
  ctx=ssl.create_default_context(cafile='/var/lib/ctlvps-agent/certs/fixture.test.crt')
  deadline=time.monotonic()+5
  while True:
   try:s=socket.create_connection(('127.0.0.1',port),timeout=5);break
   except ConnectionRefusedError:
    if time.monotonic()>deadline:raise
    time.sleep(.05)
  with s:
   with ctx.wrap_socket(s,server_hostname='fixture.test') as t:
    t.sendall(hashlib.sha224(b'synthetic-password').hexdigest().encode()+b'\r\n\x01\x01'+socket.inet_aton(host)+struct.pack('!H',18081)+b'\r\nnative-fixture')
    assert t.recv(64)==b'native-fixture'
 assert 'changed true' in apply();connect()
 print('FIXTURE IPV6 ROUTES',run('ip','-6','route','show','dev','ct-native0'),flush=True)
 print('FIXTURE IPV6 STATE',run('sysctl','-n','net.ipv6.conf.ct-native0.disable_ipv6'),flush=True)
 print('IPV6 RULE TYPES',[{k:r.get(k) for k in ['priority','table','action']} for r in json.loads(run('ip','-6','-j','rule'))],flush=True)
 with socket.create_connection(('2001:db8:123::2',18081),timeout=5) as direct:
  direct.sendall(b'baseline');assert direct.recv(64)==b'baseline'
 print('NATIVE IPv6 endpoint baseline PASS',flush=True)
 ep=lambda:run('systemctl','show','ctlvps-singbox.service','-p','InvocationID','--value')
 before=ep();assert 'changed false' in apply();assert ep()==before
 # Only the table created by this fixture is removed; controller rules remain intact.
 assert run('systemctl','is-active','ctlvps-proxy-guard.timer')=='active'
 run('nft','delete','table','inet','ctlvps_egress')
 deadline=time.monotonic()+15
 while subprocess.run(['nft','list','table','inet','ctlvps_egress'],capture_output=True).returncode:
  if time.monotonic()>deadline:raise AssertionError('native firewall guard failed to restore table')
  time.sleep(.2)
 connect();assert ep()==before
 print('NATIVE GUARD PASS: systemd timer restored fixture firewall without proxy restart.',flush=True)
 run('systemctl','stop','ctlvps-proxy-guard.timer','ctlvps-proxy-guard.service')
 policy=P('/run/ctlvps-proxy/policy.json');saved_policy=policy.read_bytes()
 try:
  policy.write_text('{}')
  run('systemctl','kill','--kill-who=all','--signal=SIGSTOP','ctlvps-singbox.service')
  result=subprocess.run(['systemctl','start','ctlvps-proxy-guard.service'],capture_output=True)
  assert result.returncode!=0
  assert run('systemctl','show','ctlvps-singbox.service','-p','MainPID','--value')=='0'
  assert not P('/run/ctlvps-proxy/egress-ready').exists()
 finally:
  policy.write_bytes(saved_policy);policy.chmod(0o600)
 run('systemctl','reset-failed','ctlvps-proxy-guard.service')
 run('systemctl','start','ctlvps-proxy-guard.service')
 run('systemctl','start','ctlvps-singbox.service','ctlvps-proxy-guard.timer');connect()
 print('NATIVE FAIL-CLOSED PASS: corrupt fixture policy revokes readiness and kills a frozen proxy on systemd 252.',flush=True)
 before=ep()
 apply('bad',False);assert ep()==before;connect()
 with socket.socket() as s:
  s.bind(('0.0.0.0',21999));s.listen();apply('collision',False)
 connect()
 print('NATIVE APPLY PASS: real launcher/systemd, verified self-signed TLS, idempotence, invalid config and failed activation rollback.',flush=True)
 cfg={'log':{'level':'error'},'inbounds':[{'type':'socks','listen':'127.0.0.1','listen_port':24005}],'outbounds':[{'type':'trojan','server':'127.0.0.1','server_port':24543,'password':'synthetic-password','tls':{'enabled':True,'server_name':'fixture.test','certificate_path':'/var/lib/ctlvps-agent/certs/fixture.test.crt'}}]}
 (BASE/'client.json').write_text(json.dumps(cfg));(BASE/'client.json').chmod(0o600)
 log=open(BASE/'client.log','w');client=subprocess.Popen(['/opt/ctlvps/bin/sing-box','run','-c',str(BASE/'client.json')],stdout=log,stderr=log);processes.append(client);time.sleep(1)
 for ipv6 in [False,True]:
  for udp in [False,True]:
   print('FORWARDING CHECK', 'IPv6' if ipv6 else 'IPv4', 'UDP' if udp else 'TCP',flush=True)
   with socket.create_connection(('127.0.0.1',24005),timeout=5) as c:
    c.sendall(b'\x05\x01\x00');assert c.recv(2)==b'\x05\x00'
    addr=b'\x04'+socket.inet_pton(socket.AF_INET6,'2001:db8:123::2') if ipv6 else b'\x01'+socket.inet_aton('198.51.100.2')
    dst=addr+struct.pack('!H',18081)
    c.sendall(bytes([5,3 if udp else 1,0])+(b'\x01'+bytes(6) if udp else dst));reply=c.recv(256);assert reply[1]==0
    if udp:
     with socket.socket(socket.AF_INET,socket.SOCK_DGRAM) as u:
      u.settimeout(5);u.sendto(bytes(3)+dst+b'native-fixture',('127.0.0.1',struct.unpack('!H',reply[-2:])[0]));assert u.recv(256).endswith(b'native-fixture')
    else:
     c.sendall(b'native-fixture');got=c.recv(64);assert got==b'native-fixture',(ipv6,udp,repr(got))
 print('NATIVE FORWARDING PASS: IPv4/IPv6 TCP/UDP with strict self-signed TLS verification.',flush=True)
 assert 'changed true' in apply('mixed');connect();connect(24544,'10.123.0.2')
 for unit,user in [('ctlvps-singbox.service','ctlvps-sb'),('ctlvps-singbox-private.service','ctlvps-sp')]:
  assert run('systemctl','show',unit,'-p','User','--value')==user
  pid=run('systemctl','show',unit,'-p','MainPID','--value');status=P('/proc',pid,'status').read_text()
  assert 'CapEff:\t0000000000002400' in status and 'NoNewPrivs:\t1' in status and 'Seccomp:\t2' in status
  assert subprocess.run(['runuser','-u',user,'--','test','-r','/var/lib/ctlvps-agent/certs/fixture.test.key']).returncode!=0
  assert subprocess.run(['runuser','-u',user,'--','test','-r','/opt/ctlvps/data/ctlvps.db']).returncode!=0
 print('NATIVE ISOLATION PASS: separate UIDs, restricted caps/seccomp, management DB and source TLS private key inaccessible.',flush=True)
 client.terminate();client.wait();log.close()
 run('systemctl','stop','ctlvps-singbox.service','ctlvps-singbox-private.service')
 assert 'changed true' in apply('snell');assert 'changed false' in apply('snell')
 pid=run('systemctl','show','ctlvps-snell@24443.service','-p','MainPID','--value');status=P('/proc',pid,'status').read_text()
 assert 'CapEff:\t0000000000000400' in status and 'NoNewPrivs:\t1' in status
 with socket.create_connection(('127.0.0.1',24443),timeout=3):pass
 print('NATIVE SNELL PASS: official core startup, dedicated UID/minimal caps and idempotence. Client authentication not tested.',flush=True)
 assert epoch()==original_epoch and run('systemctl','is-active','ctlvpsd.service')=='active'
 print('CONTROLLER PASS: production controller remained active with unchanged InvocationID.',flush=True)
except Exception:
 logpath=P('/var/log/ctlvps/sing-box.log')
 if logpath.exists():print('SYNTHETIC PROXY LOG:',logpath.read_text()[-3500:],flush=True)
 raise
finally:
 for p in processes:
  if p.poll() is None:p.terminate()
 for p in processes:
  try:p.wait(timeout=3)
  except subprocess.TimeoutExpired:p.kill();p.wait()
 cleanup()
