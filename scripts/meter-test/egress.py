"""Outbound tests in a disposable network-none privileged container only."""
import os,pathlib,socket,subprocess,sys,time
assert pathlib.Path('/.dockerenv').exists()
def run(*a):subprocess.run(a,check=True,stdout=subprocess.DEVNULL,stderr=subprocess.PIPE)
if sys.argv[1]=='probe':
    host,mark,group=sys.argv[2:];
    if group!='-':pathlib.Path('/sys/fs/cgroup'+group+'/cgroup.procs').write_text(str(os.getpid()))
    s=socket.socket();s.settimeout(.6)
    if int(mark):s.setsockopt(socket.SOL_SOCKET,socket.SO_MARK,int(mark))
    try:s.connect((host,18081));sys.exit(0)
    except OSError:sys.exit(1)
run('ip','link','set','lo','up');run('ip','netns','add','egress-peer')
procs=[]
try:
    run('ip','link','add','egress-host','type','veth','peer','name','egress-peer0');run('ip','link','set','egress-peer0','netns','egress-peer')
    for addr in ['198.51.100.1/24','10.123.0.1/24','169.254.0.1/16']:run('ip','addr','add',addr,'dev','egress-host')
    run('ip','link','set','egress-host','up')
    for addr in ['198.51.100.2/24','10.123.0.2/24','169.254.169.254/16']:run('ip','netns','exec','egress-peer','ip','addr','add',addr,'dev','egress-peer0')
    run('ip','netns','exec','egress-peer','ip','link','set','egress-peer0','up');run('ip','netns','exec','egress-peer','ip','link','set','lo','up')
    # Only TCP establishment is probed. A native listener avoids http.server's
    # reverse-DNS startup, which blocks on musl in an offline namespace.
    listener="import socket; s=socket.socket(); s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1); s.bind(('0.0.0.0',18081)); s.listen(128); exec('while True:\\n c,_=s.accept(); c.close()')"
    for prefix in [[],['ip','netns','exec','egress-peer']]:procs.append(subprocess.Popen(prefix+['python3','-c',listener],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL))
    # Interpreter startup varies across libc/base images and memory limits.
    # Prove the public listener is ready before judging the cgroup firewall.
    deadline=time.monotonic()+5
    while True:
        assert all(p.poll() is None for p in procs),'fixture HTTP server exited'
        try:
            with socket.create_connection(('198.51.100.2',18081),.2):pass
            break
        except OSError:
            assert time.monotonic()<deadline,'fixture public HTTP listener not ready'
            time.sleep(.05)
    run('nft','-f',sys.argv[1])
    def check(host,mark,group,allowed):
        p=subprocess.run(['python3',__file__,'probe',host,str(mark),group],stdout=subprocess.PIPE,stderr=subprocess.PIPE)
        assert (p.returncode==0)==allowed,(host,mark,group,p.stderr.decode(),subprocess.check_output(['nft','list','ruleset'],text=True),pathlib.Path('/proc/self/cgroup').read_text())
    for mark,group in [(0x43000001,sys.argv[2]),(0,sys.argv[2]),(0x43000003,sys.argv[2])]:
        for host in ['127.0.0.1','10.123.0.2','169.254.169.254']:check(host,mark,group,False)
        check('198.51.100.2',mark,group,mark==0x43000001)
    check('127.0.0.1',0,'-',True);check('10.123.0.2',0x43000003,'-',True)
    print('PASS cgroup sing-box: local/private/metadata denied, public requires assigned mark, zero/forged marks rejected, unrelated processes unaffected')
finally:
    for p in procs:p.terminate();p.wait()
    run('ip','netns','del','egress-peer')
    subprocess.run(['nft','delete','table','inet','ctlvps_egress'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
