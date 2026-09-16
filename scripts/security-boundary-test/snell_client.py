"""Optional OpenSnell interoperability fixture; never touches a user client."""
import pathlib, shutil, socket, struct, subprocess, time

def exact(sock, size):
    data=b''
    while len(data)<size:
        part=sock.recv(size-len(data))
        if not part: raise EOFError('SOCKS connection closed')
        data+=part
    return data

def request(port, ipv6=False, udp=False):
    with socket.create_connection(('127.0.0.1',port),timeout=4) as c:
        c.sendall(b'\x05\x01\x00');assert exact(c,2)==b'\x05\x00'
        dest=(b'\x04'+socket.inet_pton(socket.AF_INET6,'2001:db8:123::2') if ipv6 else b'\x01'+socket.inet_aton('198.51.100.2'))+struct.pack('!H',18082)
        c.sendall(bytes([5,3 if udp else 1,0])+(b'\x01'+bytes(6) if udp else dest))
        header=exact(c,4)
        if header[1]!=0: raise ConnectionError('SOCKS request rejected')
        length={1:4,4:16}.get(header[3])
        if length is None:length=exact(c,1)[0]
        bound=exact(c,length+2)
        payload=b'snell-authenticated-fixture'
        if udp:
            with socket.socket(socket.AF_INET,socket.SOCK_DGRAM) as s:
                # Pinned OpenSnell's SOCKS reply incorrectly advertises its TCP
                # listening port. Inspect its actual UDP socket in this isolated
                # fixture only; server protocol/authentication are unchanged.
                deadline=time.monotonic()+2
                while True:
                    lines=subprocess.check_output(['ss','-H','-lunp'],text=True).splitlines()
                    ports={int(line.split()[3].rsplit(':',1)[1]) for line in lines if 'snell-fixture' in line}
                    if len(ports)==1:break
                    if time.monotonic()>deadline:raise AssertionError('ambiguous fixture UDP socket')
                    time.sleep(.05)
                s.settimeout(4);s.sendto(bytes(3)+dest+payload,('127.0.0.1',ports.pop()))
                assert s.recv(512).endswith(payload)
        else:
            c.sendall(payload);assert exact(c,len(payload))==payload

def verify_snell_client():
    source=pathlib.Path('/fixtures/snell-client')
    if not source.exists():
        print('SNELL CLIENT SKIP: optional client fixture absent');return
    binary='/usr/local/bin/snell-fixture-client';shutil.copyfile(source,binary);pathlib.Path(binary).chmod(0o755)
    echo="""import socket,threading,time
for family in (socket.AF_INET,socket.AF_INET6):
 def tcp(family=family):
  s=socket.socket(family);s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1)
  if family==socket.AF_INET6:s.setsockopt(socket.IPPROTO_IPV6,socket.IPV6_V6ONLY,1)
  s.bind(('::' if family==socket.AF_INET6 else '0.0.0.0',18082));s.listen()
  while True:
   c,_=s.accept()
   with c:c.sendall(c.recv(128))
 def udp(family=family):
  s=socket.socket(family,socket.SOCK_DGRAM)
  if family==socket.AF_INET6:s.setsockopt(socket.IPPROTO_IPV6,socket.IPV6_V6ONLY,1)
  s.bind(('::' if family==socket.AF_INET6 else '0.0.0.0',18082))
  while True:
   d,a=s.recvfrom(128);s.sendto(d,a)
 threading.Thread(target=tcp,daemon=True).start();threading.Thread(target=udp,daemon=True).start()
while True:time.sleep(1)
"""
    processes=[subprocess.Popen(['ip','netns','exec','boundary-peer','python3','-c',echo])]
    logs=[]
    try:
        for port,psk in [(24009,'synthetic-snell-fixture'),(24010,'wrong-synthetic-key')]:
            config=pathlib.Path('/tmp/snell-client-'+str(port)+'.conf')
            config.write_text('[snell-client]\nlisten = 127.0.0.1:'+str(port)+'\nserver = 127.0.0.1:24443\npsk = '+psk+'\nversion = v5\nobfs = off\nreuse = true\ntfo = false\n');config.chmod(0o644)
            log=open('/tmp/snell-client-'+str(port)+'.log','w');logs.append(log)
            p=subprocess.Popen(['runuser','-u','nobody','--',binary,'-v','-c',str(config)],stdout=log,stderr=log);processes.append(p)
            for _ in range(50):
                if p.poll() is not None:raise AssertionError(pathlib.Path('/tmp/snell-client-'+str(port)+'.log').read_text())
                try:
                    with socket.create_connection(('127.0.0.1',port),timeout=.1):break
                except OSError:time.sleep(.1)
            else:raise AssertionError('Snell client did not listen')
        for ipv6 in (False,True):
            for udp in (False,True):
                try:request(24009,ipv6,udp);time.sleep(.2)
                except Exception as e:
                    raise AssertionError((ipv6,udp,pathlib.Path('/tmp/snell-client-24009.log').read_text())) from e
        request(24009);request(24009)
        try:request(24010)
        except (OSError,EOFError):pass
        else:raise AssertionError('wrong PSK forwarded traffic')
        print('SNELL CLIENT PASS: official server 5.0.1, authenticated IPv4/IPv6 TCP/UDP, TCP reuse, wrong PSK rejection; OpenSnell TCP transport, QUIC mode excluded.')
    finally:
        for p in processes:
            if p.poll() is None:p.terminate()
        for p in processes:
            try:p.wait(timeout=3)
            except subprocess.TimeoutExpired:p.kill();p.wait()
        for log in logs:log.close()
