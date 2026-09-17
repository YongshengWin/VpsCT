"""Isolated, synthetic TCP/UDP client <-> proxy <-> origin counter test.
Run only in the disposable container launched by test-meter-container.sh.
"""
import subprocess,sys,json,time,os,socket,threading,atexit,textwrap
from pathlib import Path
rules=Path(sys.argv[1])
def run(*a):return subprocess.check_output(a,text=True)
for ns in ('client','origin'):
 run('ip','netns','add',ns)
 run('ip','link','add','v-'+ns,'type','veth','peer','name','peer-'+ns)
 run('ip','link','set','peer-'+ns,'netns',ns)
 idx=1 if ns=='client' else 2
 run('ip','addr','add',f'192.0.{idx}.1/24','dev','v-'+ns)
 run('ip','-6','addr','add',f'2001:db8:{idx}::1/64','dev','v-'+ns,'nodad')
 run('ip','link','set','v-'+ns,'up')
 run('ip','netns','exec',ns,'ip','addr','add',f'192.0.{idx}.2/24','dev','peer-'+ns)
 run('ip','netns','exec',ns,'ip','-6','addr','add',f'2001:db8:{idx}::2/64','dev','peer-'+ns,'nodad')
 run('ip','netns','exec',ns,'ip','link','set','peer-'+ns,'up')
 run('ip','netns','exec',ns,'ip','link','set','lo','up')
run('ip','route','add','default','via','192.0.2.2')
run('ip','netns','exec','client','ip','route','add','default','via','192.0.1.1')
run('nft','-f',str(rules/'active.nft'))
subprocess.run(['nft','-f','-'],input='add table inet reference\nadd counter inet reference rx\nadd counter inet reference tx\nadd chain inet reference input { type filter hook input priority -150; }\nadd chain inet reference output { type filter hook output priority -150; }\nadd rule inet reference input iifname { "v-client", "v-origin" } meta l4proto { tcp, udp } counter name rx\nadd rule inet reference output oifname { "v-client", "v-origin" } meta l4proto { tcp, udp } counter name tx\n',text=True,check=True)
origin=r'''
import socket,threading
for kind in (socket.SOCK_STREAM,socket.SOCK_DGRAM):
 s=socket.socket(socket.AF_INET6,kind);s.setsockopt(socket.IPPROTO_IPV6,socket.IPV6_V6ONLY,0);s.bind(('::',22000))
 if kind==socket.SOCK_STREAM:
  s.listen()
  def tcp(s):
   while True:
    c,_=s.accept()
    def conn(c):
     with c:
      while True:
       d=c.recv(65536)
       if not d:break
       c.sendall(d)
    threading.Thread(target=conn,args=(c,),daemon=True).start()
  threading.Thread(target=tcp,args=(s,),daemon=True).start()
 else:
  def udp(s):
   while True:
    d,a=s.recvfrom(65536);s.sendto(d,a)
  threading.Thread(target=udp,args=(s,),daemon=True).start()
threading.Event().wait()
'''
procs=[subprocess.Popen(['ip','netns','exec','origin','python3','-c',origin])]
def cleanup():
 for p in procs:
  p.terminate()
 for p in procs:
  try:p.wait(timeout=3)
  except subprocess.TimeoutExpired:p.kill();p.wait()
atexit.register(cleanup)
binary=os.environ.get('CTLVPS_SINGBOX')
if binary:
 config={"log":{"level":"error"},"inbounds":[],"outbounds":[],"route":{"rules":[]}}
 for node in (1,2):
  config['inbounds'].append({"type":"shadowsocks","tag":f"node-{node}","listen":"::","listen_port":21000+node,"method":"aes-128-gcm","password":"isolated-fixture"})
  config['outbounds'].append({"type":"direct","tag":f"direct-{node}","routing_mark":0x43000000|node})
  config['route']['rules'].append({"inbound":[f"node-{node}"],"action":"route","outbound":f"direct-{node}"})
 cfg=rules/'server.json';cfg.write_text(json.dumps(config))
 procs.append(subprocess.Popen([binary,'run','-c',str(cfg)]))
 for node in (1,2):
  config={"log":{"level":"error"},"inbounds":[{"type":"socks","listen":"127.0.0.1","listen_port":23000+node}],"outbounds":[{"type":"shadowsocks","server":"192.0.1.1","server_port":21000+node,"method":"aes-128-gcm","password":"isolated-fixture"}]}
  cfg=rules/f'client-{node}.json';cfg.write_text(json.dumps(config))
  procs.append(subprocess.Popen(['ip','netns','exec','client',binary,'run','-c',str(cfg)]))
else:
 # Synthetic direct proxy exercises the same SO_MARK/conntrack primitive.
 for node in (1,2):
  for kind in (socket.SOCK_STREAM,socket.SOCK_DGRAM):
   s=socket.socket(socket.AF_INET6,kind);s.setsockopt(socket.IPPROTO_IPV6,socket.IPV6_V6ONLY,0);s.bind(('::',21000+node))
   if kind==socket.SOCK_STREAM:
    s.listen()
    def accept(s,node):
     while True:
      c,_=s.accept()
      def relay(c):
       o=socket.socket(socket.AF_INET6);o.setsockopt(socket.SOL_SOCKET,36,0x43000000|node);o.connect(('2001:db8:2::2',22000))
       with c,o:
        while True:
         d=c.recv(65536)
         if not d:break
         o.sendall(d);left=len(d)
         while left:
          chunk=o.recv(left)
          if not chunk: return
          c.sendall(chunk);left-=len(chunk)
      threading.Thread(target=relay,args=(c,),daemon=True).start()
    threading.Thread(target=accept,args=(s,node),daemon=True).start()
   else:
    def relay_udp(s,node):
     o=socket.socket(socket.AF_INET6,socket.SOCK_DGRAM);o.setsockopt(socket.SOL_SOCKET,36,0x43000000|node);o.settimeout(2)
     while True:
      d,a=s.recvfrom(65536)
      try:
       o.sendto(d,('2001:db8:2::2',22000));r,_=o.recvfrom(65536);s.sendto(r,a)
      except (PermissionError,TimeoutError):pass
    threading.Thread(target=relay_udp,args=(s,node),daemon=True).start()
time.sleep(.6)
def snapshot(table='ctlvps_nodes'):
 d=json.loads(run('nft','-j','list','counters','table','inet',table))
 return {x['counter']['name']:x['counter']['bytes'] for x in d['nftables'] if 'counter'in x}
def transfer(node,udp=False,blocked=False,ipv6=False):
 if binary:
  code=f"""
import socket,struct
blocked={blocked!r}
try:
 c=socket.create_connection(('127.0.0.1',{23000+node}),timeout=1)
 c.sendall(bytes([5,1,0]));assert c.recv(2)==bytes([5,0])
 addr=bytes([4])+socket.inet_pton(socket.AF_INET6,'2001:db8:2::2') if {ipv6!r} else bytes([1])+socket.inet_aton('192.0.2.2')
 dest=addr+struct.pack('!H',22000)
 c.sendall(bytes([5,{3 if udp else 1},0])+(bytes([1,0,0,0,0,0,0]) if {udp!r} else dest))
 reply=c.recv(256);assert reply[1]==0
 if {udp!r}:
  port=struct.unpack('!H',reply[-2:])[0]
  s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);s.settimeout(1)
  for _ in range(16):
   s.sendto(bytes(3)+dest+b'x'*512,('127.0.0.1',port))
   data=s.recv(20000);assert data.endswith(b'x'*512)
 else:
  c.sendall(b'x'*8192);data=b''
  while len(data)<8192:
   chunk=c.recv(8192)
   if not chunk:raise ConnectionError('EOF')
   data+=chunk
  assert data==b'x'*8192
 assert not blocked
except (TimeoutError,ConnectionError):
 assert blocked
"""
 else:
  code=f'''
 import socket
 s=socket.socket({'socket.AF_INET6' if ipv6 else 'socket.AF_INET'},{'socket.SOCK_DGRAM' if udp else 'socket.SOCK_STREAM'});s.settimeout(1)
 try:
  s.connect(({'2001:db8:1::1' if ipv6 else '192.0.1.1'!r},{21000+node}));s.sendall(b'x'*8192);data=b''
  while len(data)<8192:data+=s.recv(8192)
  assert len(data)==8192
  assert not {blocked}
 except (TimeoutError,ConnectionRefusedError):
  assert {blocked}
 '''
 subprocess.run(['ip','netns','exec','client','python3','-c',textwrap.dedent(code)],check=True)
for node in (1,2):
 for udp in (False,True):
  for ipv6 in (False,True):
   before=snapshot();ref_before=snapshot('reference')
   transfer(node,udp,ipv6=ipv6);time.sleep(.2)
   after=snapshot();ref_after=snapshot('reference')
   for direction in ('rx','tx'):
    delta=after[f'n{node}_{direction}']-before[f'n{node}_{direction}']
    reference=ref_after[direction]-ref_before[direction]
    assert delta==reference,(node,udp,ipv6,direction,delta,reference)
    assert delta>=2*8192,(node,udp,ipv6,direction,delta)
    other=3-node;assert after[f'n{other}_{direction}']==before[f'n{other}_{direction}'],'cross billing'
# Reapplying rules preserves both counter objects.
before=snapshot();run('nft','-f',str(rules/'active.nft'));assert snapshot()==before
run('nft','-f',str(rules/'blocked.nft'))
transfer(1,blocked=True);transfer(1,udp=True,blocked=True);transfer(2)
# Retired counters must freeze while other nodes keep carrying traffic.
run('nft','-f',str(rules/'retired.nft'))
before=snapshot()
transfer(1,blocked=True);transfer(1,udp=True,blocked=True);transfer(2)
after=snapshot()
for direction in ('rx','tx'):
 assert after[f'n1_{direction}']==before[f'n1_{direction}'],'retired counter not frozen'
 assert after[f'n2_{direction}']>before[f'n2_{direction}'],'active node interrupted'
print('PASS retired counters frozen with active traffic continuing')
print(('sing-box: ' if binary else 'synthetic: ')+'PASS IPv4/IPv6 TCP/UDP exact kernel byte equality on both legs; per-node isolation; counters survive rule replacement; node-only blocking')
for p in procs:p.terminate()
