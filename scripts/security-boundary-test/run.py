"""Synthetic probes under actual generated proxy units; NOT a remote exploit.
All destinations, credentials and the Unix listener belong to this offline container.
"""
import json, os, pathlib, socket, subprocess, sys, threading, time
P=pathlib.Path
CANDIDATE=os.environ.get("CTLVPS_CANDIDATE")=="1"
assert P('/.dockerenv').exists() and P('/proc/1/comm').read_text().strip()=='systemd'

def run(*a):
    return subprocess.run(a,check=True,stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True).stdout.strip()

FILES={'agent_state':'/var/lib/ctlvps-agent/state.json',
       'trust_state':'/var/lib/ctlvps-security/fixture.json',
       'maintenance_state':'/var/lib/ctlvps-maintenance/fixture.json'}
TARGETS={'loopback':'127.0.0.1','private':'10.123.0.2',
         'metadata_fixture':'169.254.169.254','public_fixture':'198.51.100.2'}
MARKS={'assigned':0x43000001,'zero':0,'forged_unassigned':0x43000002,'forged_private_node':0x43000003}

def probe(kind):
    if CANDIDATE:
        assert "Seccomp:\t2" in P("/proc/self/status").read_text()
    out={'kind':kind,'uid':os.geteuid(),'files':{},'network':{}}
    out['cgroup']=P('/proc/self/cgroup').read_text().strip()
    out['capabilities']=[l for l in P('/proc/self/status').read_text().splitlines() if l.startswith(('CapEff:','NoNewPrivs:'))]
    for name,path in FILES.items():
        result={}
        for label,flags in [('read',os.O_RDONLY),('write',os.O_WRONLY)]:
            try:fd=os.open(path,flags);os.close(fd);result[label]=True
            except OSError:result[label]=False
        out['files'][name]=result
    try:
        with socket.socket(socket.AF_UNIX) as s:
            s.settimeout(1);s.connect('/run/ctlvps-maintenance/control.sock');s.sendall(b'fixture');out['maintenance_socket']=s.recv(16)==b'fixture-only'
    except OSError:out['maintenance_socket']=False
    for mark_name,mark in MARKS.items():
        row={}
        for name,host in TARGETS.items():
            for transport,kind_value in [('tcp',socket.SOCK_STREAM),('udp',socket.SOCK_DGRAM)]:
                with socket.socket(socket.AF_INET,kind_value) as s:
                    s.settimeout(.35);s.setsockopt(socket.SOL_SOCKET,socket.SO_MARK,mark)
                    try:
                        s.connect((host,18081));s.sendall(b'boundary-fixture');row[name+'_'+transport]=s.recv(64)==b'boundary-fixture'
                    except OSError:row[name+'_'+transport]=False
        out['network'][mark_name]=row
    # Create an unrelated empty canary table: verifies authority without changing
    # the fixture's protection rules or any non-product host configuration.
    p=subprocess.run(['nft','add','table','inet','boundary_'+kind],stdout=subprocess.PIPE,stderr=subprocess.PIPE)
    out['can_modify_nft']=p.returncode==0
    out['raw_sockets']={}
    for name,family,protocol in [('ipv4',socket.AF_INET,socket.IPPROTO_ICMP),('ipv6',socket.AF_INET6,socket.IPPROTO_ICMPV6),('packet',socket.AF_PACKET,0),('netlink',socket.AF_NETLINK,0)]:
        try:
            with socket.socket(family,socket.SOCK_RAW,protocol):out['raw_sockets'][name]=True
        except OSError:out['raw_sockets'][name]=False
    if p.returncode==0:run('nft','delete','table','inet','boundary_'+kind)
    P('/var/log/ctlvps/'+kind+'-boundary.json').write_text(json.dumps(out))

if len(sys.argv)>1:
    probe(sys.argv[2]);sys.exit(0)

for path in FILES.values():
    P(path).parent.mkdir(parents=True,exist_ok=True,mode=0o700)
    P(path).parent.chmod(0o700);P(path).write_text('{"synthetic":true}');P(path).chmod(0o600)
P('/var/log/ctlvps').mkdir(parents=True,exist_ok=True)
P('/run/ctlvps-maintenance').mkdir(mode=0o700)
unix=socket.socket(socket.AF_UNIX);unix.bind('/run/ctlvps-maintenance/control.sock');os.chmod('/run/ctlvps-maintenance/control.sock',0o600);unix.listen()
def serve_unix():
    while True:
        c,_=unix.accept()
        with c:c.recv(64);c.sendall(b'fixture-only')
threading.Thread(target=serve_unix,daemon=True).start()
# The same files must be inaccessible to an unrelated unprivileged account.
for path in FILES.values():
    p=subprocess.run(['runuser','-u','nobody','--','test','-r',path],stdout=subprocess.PIPE,stderr=subprocess.PIPE)
    assert p.returncode!=0,'invalid file-permission negative control'

if CANDIDATE:
    run('useradd','--system','--user-group','--no-create-home','--shell','/usr/sbin/nologin','ctlvps-probe')
    run('chown','ctlvps-probe:ctlvps-probe','/var/log/ctlvps')

run('ip','link','set','lo','up');run('ip','netns','add','boundary-peer')
run('ip','link','add','boundary-host','type','veth','peer','name','boundary-peer0')
run('ip','link','set','boundary-peer0','netns','boundary-peer')
for addr in ['198.51.100.1/24','10.123.0.1/24','169.254.0.1/16']:run('ip','addr','add',addr,'dev','boundary-host')
run('ip','link','set','boundary-host','up')
for addr in ['198.51.100.2/24','10.123.0.2/24','169.254.169.254/16']:run('ip','netns','exec','boundary-peer','ip','addr','add',addr,'dev','boundary-peer0')
run('ip','netns','exec','boundary-peer','ip','link','set','boundary-peer0','up')
run('ip','netns','exec','boundary-peer','ip','link','set','lo','up')
echo='''import socket,threading
s=socket.socket();s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1);s.bind(('0.0.0.0',18081));s.listen()
def tcp():
 while True:
  c,_=s.accept()
  with c:c.sendall(c.recv(64))
threading.Thread(target=tcp,daemon=True).start()
u=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);u.bind(('0.0.0.0',18081))
while True:
 d,a=u.recvfrom(64);u.sendto(d,a)
'''
procs=[subprocess.Popen(prefix+['python3','-c',echo]) for prefix in [[],['ip','netns','exec','boundary-peer']]]
try:
    for name in ['ctlvps-proxy.slice','ctlvps-proxy-n2.slice','ctlvps-proxy-public.slice','ctlvps-proxy-private.slice']:
        P('/etc/systemd/system/'+name).write_text('[Unit]\nDescription=Boundary fixture\n[Slice]\nIPAccounting=yes\n')
    for kind in (['singbox','snell','private'] if CANDIDATE else ['singbox','snell']):
        P('/etc/systemd/system/ctlvps-boundary-'+kind+'.service').write_bytes(P('/fixtures/'+kind+'.service').read_bytes())
    run('systemctl','daemon-reload');run('systemctl','start','ctlvps-proxy-n2.slice','ctlvps-proxy-public.slice','ctlvps-proxy-private.slice')
    group=run('systemctl','show','ctlvps-proxy-n2.slice','-p','ControlGroup','--value')
    assert group=='/ctlvps.slice/ctlvps-proxy.slice/ctlvps-proxy-n2.slice',group
    run('nft','-f','/fixtures/egress.nft');time.sleep(.3)
    results=[]
    for kind in (['singbox','snell','private'] if CANDIDATE else ['singbox','snell']):
        run('systemctl','start','ctlvps-boundary-'+kind+'.service')
        output=P('/var/log/ctlvps/'+kind+'-boundary.json')
        deadline=time.monotonic()+40
        while not output.exists() and time.monotonic()<deadline:time.sleep(.2)
        if not output.exists():raise AssertionError(run('journalctl','-u','ctlvps-boundary-'+kind+'.service','--no-pager','-n','15'))
        result=json.loads(output.read_text());results.append(result)
        if CANDIDATE:
            assert result['uid']!=0 and all(not v['read'] and not v['write'] for v in result['files'].values())
            assert not result['maintenance_socket'] and not result['can_modify_nft']
            assert result['raw_sockets']=={'ipv4':False,'ipv6':False,'packet':False,'netlink':True}
        else:
            assert result['uid']==0 and all(v['read'] for v in result['files'].values())
            assert result['files']['agent_state']['write'] and not result['files']['trust_state']['write']
            assert result['maintenance_socket'] and result['can_modify_nft']
        for mark,row in result['network'].items():
            for name,reachable in row.items():
                expected=name.startswith('public_fixture') or (kind=='private' if CANDIDATE else kind=='singbox' and mark!='assigned')
                if CANDIDATE and kind!='snell':
                    valid_mark = 'forged_private_node' if kind=='private' else 'assigned'
                    expected = expected and mark==valid_mark
                assert reachable==expected,(kind,mark,name,reachable,expected)
    print(json.dumps({'kernel':run('uname','-r'),'arch':run('uname','-m'),'results':results},indent=2))
    if CANDIDATE:
        print('CANDIDATE PASS: nonroot, SO_MARK without NET_ADMIN, raw socket restrictions, management access denied, cgroup egress and separate private group work.')
        if P('/fixtures/sing-box').exists():
            from tls_candidate import verify_tls
            verify_tls(run)
            from production import verify_production
            verify_production(run)
    else:print('REPRODUCED: proxy root can read management fixtures; sing-box mark changes bypass destination guard; Snell cgroup guard resists mark changes. Both units retain nft modification authority.')
finally:
    for p in procs:p.terminate();p.wait()
