"""All deployed sing-box protocols through the hardened production service."""
import pathlib,socket,struct,subprocess,time

def verify_protocols(run,apply):
    with open('/etc/hosts','a') as f:f.write('\n198.51.100.2 fixture.test\n')
    run('ip','-6','addr','add','2001:db8:123::1/64','dev','boundary-host','nodad')
    run('ip','netns','exec','boundary-peer','ip','-6','addr','add','2001:db8:123::2/64','dev','boundary-peer0','nodad')
    echo="""import socket,threading
s=socket.socket(socket.AF_INET6);s.setsockopt(socket.IPPROTO_IPV6,socket.IPV6_V6ONLY,1);s.bind(('::',18081));s.listen()
def tcp():
 while True:
  c,_=s.accept()
  with c:c.sendall(c.recv(64))
threading.Thread(target=tcp,daemon=True).start()
u=socket.socket(socket.AF_INET6,socket.SOCK_DGRAM);u.setsockopt(socket.IPPROTO_IPV6,socket.IPV6_V6ONLY,1);u.bind(('::',18081))
while True:
 d,a=u.recvfrom(64);u.sendto(d,a)
"""
    processes=[subprocess.Popen(['ip','netns','exec','boundary-peer','python3','-c',echo]),
      subprocess.Popen(['ip','netns','exec','boundary-peer','openssl','s_server','-accept','8443','-cert','/var/lib/ctlvps-agent/source.crt','-key','/var/lib/ctlvps-agent/source.key','-tls1_3','-www'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)]
    try:
        apply('all')
        for i,proto in enumerate(['vless','anytls','hysteria2','tuic','trojan','ss']):
            logfile=open('/tmp/client-'+proto+'.log','w')
            p=subprocess.Popen(['/opt/ctlvps/bin/sing-box','run','-c','/tmp/client-'+proto+'.json'],stdout=logfile,stderr=logfile);processes.append(p)
            time.sleep(.25)
            for ipv6 in (False,True):
                for udp in (False,True):
                    try:
                        with socket.create_connection(('127.0.0.1',24001+i),timeout=8) as c:
                            c.sendall(b'\x05\x01\x00');assert c.recv(2)==b'\x05\x00'
                            addr=b'\x04'+socket.inet_pton(socket.AF_INET6,'2001:db8:123::2') if ipv6 else b'\x01'+socket.inet_aton('198.51.100.2')
                            dest=addr+struct.pack('!H',18081)
                            c.sendall(bytes([5,3 if udp else 1,0])+(b'\x01'+bytes(6) if udp else dest))
                            reply=c.recv(256);assert reply[1]==0
                            if udp:
                                port=struct.unpack('!H',reply[-2:])[0]
                                with socket.socket(socket.AF_INET,socket.SOCK_DGRAM) as u:
                                    u.settimeout(8);u.sendto(bytes(3)+dest+b'protocol-fixture',('127.0.0.1',port));assert u.recv(256).endswith(b'protocol-fixture')
                            else:
                                c.sendall(b'protocol-fixture');assert c.recv(64)==b'protocol-fixture'
                    except Exception as e:
                        raise AssertionError((proto,ipv6,udp,pathlib.Path('/tmp/client-'+proto+'.log').read_text())) from e
            p.terminate();p.wait();logfile.close()
            print('PROTOCOL PASS:',proto,'IPv4/IPv6 TCP/UDP via production sandbox')
    finally:
        for p in processes:
            if p.poll() is None:p.terminate()
        for p in processes:
            try:p.wait(timeout=3)
            except subprocess.TimeoutExpired:p.kill();p.wait()
