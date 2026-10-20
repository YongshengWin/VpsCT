"""Exercise NAT, node/forward accounting and admission in a disposable container."""

import atexit
import json
import socket
import subprocess
import sys
import threading
import time
from pathlib import Path

fixtures = Path(sys.argv[1])
clients = []


def run(*args):
    return subprocess.check_output(args, text=True)


def apply(filename):
    # Check syntax separately before changing this disposable namespace.
    run("nft", "--check", "-f", str(fixtures / filename))
    run("nft", "-f", str(fixtures / filename))


def snapshot(table="ctlvps_nodes"):
    doc = json.loads(run("nft", "-j", "list", "counters", "table", "inet", table))
    return {
        row["counter"]["name"]: row["counter"]["bytes"]
        for row in doc["nftables"]
        if "counter" in row
    }


def cleanup():
    for client in clients:
        if client.poll() is None:
            client.terminate()
    for client in clients:
        try:
            client.wait(timeout=2)
        except subprocess.TimeoutExpired:
            client.kill()
            client.wait()
    subprocess.run(["ip", "netns", "del", "nat-client"], capture_output=True)


atexit.register(cleanup)
run("ip", "netns", "add", "nat-client")
run("ip", "link", "add", "v-nat", "type", "veth", "peer", "name", "peer-nat")
run("ip", "link", "set", "peer-nat", "netns", "nat-client")
for address in ("192.0.1.1/24", "192.0.1.3/24"):
    run("ip", "addr", "add", address, "dev", "v-nat")
run("ip", "-6", "addr", "add", "2001:db8:1::1/64", "dev", "v-nat", "nodad")
run("ip", "link", "set", "v-nat", "up")
run("ip", "netns", "exec", "nat-client", "ip", "addr", "add", "192.0.1.2/24", "dev", "peer-nat")
run("ip", "netns", "exec", "nat-client", "ip", "-6", "addr", "add", "2001:db8:1::2/64", "dev", "peer-nat", "nodad")
run("ip", "netns", "exec", "nat-client", "ip", "link", "set", "peer-nat", "up")
run("ip", "netns", "exec", "nat-client", "ip", "link", "set", "lo", "up")

apply("active.nft")
reference = """add table inet nat_reference
add chain inet nat_reference input { type filter hook input priority -150; policy accept; }
add chain inet nat_reference output { type filter hook output priority -150; policy accept; }
"""
for node, address in ((1, "192.0.1.3"), (2, "192.0.1.1")):
    for direction in ("rx", "tx"):
        reference += f"add counter inet nat_reference n{node}_{direction}\n"
    reference += f'add rule inet nat_reference input iifname "v-nat" ip daddr {address} meta l4proto {{ tcp, udp }} th dport {30000 + node} counter name n{node}_rx\n'
    reference += f'add rule inet nat_reference output oifname "v-nat" ip saddr {address} meta l4proto {{ tcp, udp }} th sport {30000 + node} counter name n{node}_tx\n'
for forward in (1, 2):
    for direction in ("rx", "tx"):
        reference += f"add counter inet nat_reference f{forward}_{direction}\n"
    reference += f'add rule inet nat_reference input iifname "v-nat" ip daddr 192.0.1.1 meta l4proto {{ tcp, udp }} th dport {31000 + forward} counter name f{forward}_rx\n'
    reference += f'add rule inet nat_reference output oifname "v-nat" ip saddr 192.0.1.1 meta l4proto {{ tcp, udp }} th sport {31000 + forward} counter name f{forward}_tx\n'
subprocess.run(["nft", "-f", "-"], input=reference, text=True, check=True)


def echo(address, port):
    family = socket.AF_INET6 if ":" in address else socket.AF_INET
    prefix = f"{address}:{port}|".encode()
    for kind in (socket.SOCK_STREAM, socket.SOCK_DGRAM):
        server = socket.socket(family, kind)
        if family == socket.AF_INET6:
            server.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_V6ONLY, 1)
        server.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        server.bind((address, port))
        if kind == socket.SOCK_STREAM:
            server.listen()

            def accept(server=server, prefix=prefix):
                while True:
                    connection, _ = server.accept()

                    def respond(connection, prefix):
                        try:
                            with connection:
                                while data := connection.recv(65536):
                                    connection.sendall(prefix + data)
                        except (ConnectionError, OSError):
                            pass

                    threading.Thread(target=respond, args=(connection, prefix), daemon=True).start()

            threading.Thread(target=accept, daemon=True).start()
        else:

            def datagram(server=server, prefix=prefix):
                while True:
                    data, peer = server.recvfrom(65536)
                    server.sendto(prefix + data, peer)

            threading.Thread(target=datagram, daemon=True).start()


# Node 1 binds the secondary address: REDIRECT would select the primary address
# and fail, whereas port-only DNAT retains this destination. Other fixtures are
# unrelated listeners on an unused mapping and on the same IPv6 local port.
echo("192.0.1.3", 30001)
echo("192.0.1.1", 30002)
echo("192.0.1.1", 31001)
echo("192.0.1.1", 31002)
echo("192.0.1.1", 10003)
echo("2001:db8:1::1", 10001)

client_program = r'''
import json,socket,struct,sys
connections={}
for line in sys.stdin:
 request=json.loads(line)
 try:
  key=request['key']
  if key not in connections:
   family=socket.AF_INET6 if ':' in request['address'] else socket.AF_INET
   kind=socket.SOCK_DGRAM if request['udp'] else socket.SOCK_STREAM
   connection=socket.socket(family,kind);connection.settimeout(.55)
   if kind==socket.SOCK_STREAM:
    connection.setsockopt(socket.SOL_SOCKET,socket.SO_LINGER,struct.pack('ii',1,0))
   connection.connect((request['address'],request['port']))
   connections[key]=connection
  connection=connections[key]
  body=b'x'*1024
  connection.sendall(body)
  expected=request['prefix'].encode()+body
  if connection.type==socket.SOCK_DGRAM:
   reply=connection.recv(65536)
  else:
   reply=b''
   while len(reply)<len(expected):
    chunk=connection.recv(len(expected)-len(reply))
    if not chunk:raise ConnectionError('EOF')
    reply+=chunk
  assert reply==expected,(reply[:80],expected[:80])
  print(json.dumps({'ok':True}),flush=True)
 except (TimeoutError,ConnectionError,OSError):
  print(json.dumps({'ok':False}),flush=True)
'''


def new_client():
    client = subprocess.Popen(
        ["ip", "netns", "exec", "nat-client", "python3", "-u", "-c", client_program],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        text=True,
    )
    clients.append(client)
    return client


def exchange(client, key, node=1, udp=False, allowed=True, address=None, port=None, prefix=None):
    address = address or ("192.0.1.3" if node == 1 else "192.0.1.1")
    port = port or (10000 + node)
    prefix = prefix or f"{address}:{30000 + node}|"
    request = {"key": key, "address": address, "port": port, "prefix": prefix, "udp": udp}
    client.stdin.write(json.dumps(request) + "\n")
    client.stdin.flush()
    reply = client.stdout.readline()
    assert reply, ("client exited", client.poll(), request)
    assert json.loads(reply)["ok"] == allowed, (request, allowed, reply)


client = new_client()
for node in (1, 2):
    for udp in (False, True):
        before, ref_before = snapshot(), snapshot("nat_reference")
        exchange(client, f"node-{node}-{udp}", node=node, udp=udp)
        time.sleep(.2)  # Include delayed TCP ACKs in both independent readings.
        after, ref_after = snapshot(), snapshot("nat_reference")
        for direction in ("rx", "tx"):
            name = f"n{node}_{direction}"
            billed = after[name] - before[name]
            measured = ref_after[name] - ref_before[name]
            assert billed == measured and billed >= 1024, (node, udp, direction, billed, measured)
            other = f"n{3-node}_{direction}"
            assert after[other] == before[other], ("cross-node billing", node, udp, direction)
print("PASS IPv4 TCP/UDP port-only DNAT; secondary address preserved; exact node/reference RX/TX bytes")

def forward_exchange(key, forward=1, udp=False, allowed=True):
    exchange(client, key, udp=udp, allowed=allowed, address="192.0.1.1",
             port=11000 + forward, prefix=f"192.0.1.1:{31000 + forward}|")


time.sleep(.2)
node_before = snapshot()
for forward in (1, 2):
    for udp in (False, True):
        before, ref_before = snapshot("ctlvps_forwards"), snapshot("nat_reference")
        forward_exchange(f"forward-{forward}-{udp}", forward=forward, udp=udp)
        time.sleep(.2)
        after, ref_after = snapshot("ctlvps_forwards"), snapshot("nat_reference")
        for direction in ("rx", "tx"):
            name = f"f{forward}_{direction}"
            billed, measured = after[name] - before[name], ref_after[name] - ref_before[name]
            assert billed == measured and billed >= 1024, (forward, udp, direction, billed, measured)
            other = f"f{3-forward}_{direction}"
            assert after[other] == before[other], ("cross-forward billing", forward, udp, direction)
assert snapshot() == node_before, "forward NAT traffic billed to a node"
print("PASS fixed-forward TCP/UDP NAT keeps exact independent RX/TX counters")

before = snapshot()
forward_before = snapshot("ctlvps_forwards")
apply("active.nft")
assert snapshot() == before, "rule replay reset node counters"
assert snapshot("ctlvps_forwards") == forward_before, "rule replay reset forward counters"
for udp in (False, True):
    exchange(client, f"node-1-{udp}", udp=udp)
    forward_exchange(f"forward-1-{udp}", udp=udp)
print("PASS NAT/measurement rule replay preserves counters and existing connections")

time.sleep(.2)
before = snapshot()
for udp in (False, True):
    exchange(client, f"unused-{udp}", udp=udp, address="192.0.1.1", port=10003, prefix="192.0.1.1:10003|")
    exchange(client, f"ipv6-{udp}", udp=udp, address="2001:db8:1::1", port=10001, prefix="2001:db8:1::1:10001|")
time.sleep(.2)
assert snapshot() == before, "unallocated mapping or IPv6 captured as a NAT node"
print("PASS unallocated mappings and direct IPv6 listeners remain independent")

apply("blocked.nft")
for udp in (False, True):
    exchange(client, f"node-1-{udp}", udp=udp, allowed=False)
    exchange(client, f"blocked-new-{udp}", udp=udp, allowed=False)
    exchange(client, f"node-2-{udp}", node=2, udp=udp)
    forward_exchange(f"forward-1-{udp}", udp=udp, allowed=False)
    forward_exchange(f"blocked-forward-new-{udp}", udp=udp, allowed=False)
    forward_exchange(f"forward-2-{udp}", forward=2, udp=udp)
print("PASS blocked node rejects existing NAT conntrack and new TCP/UDP while other nodes continue")
print("PASS blocked forward rejects existing NAT conntrack and new TCP/UDP independently")

apply("retired.nft")
time.sleep(.1)
before = snapshot()
forward_before = snapshot("ctlvps_forwards")
for udp in (False, True):
    exchange(client, f"node-1-{udp}", udp=udp, allowed=False)
    exchange(client, f"retired-new-{udp}", udp=udp, allowed=False)
    exchange(client, f"node-2-{udp}", node=2, udp=udp)
    forward_exchange(f"forward-1-{udp}", udp=udp, allowed=False)
    forward_exchange(f"retired-forward-new-{udp}", udp=udp, allowed=False)
    forward_exchange(f"forward-2-{udp}", forward=2, udp=udp)
after = snapshot()
forward_after = snapshot("ctlvps_forwards")
for direction in ("rx", "tx"):
    assert after[f"n1_{direction}"] == before[f"n1_{direction}"], "retired node counter changed"
    assert after[f"n2_{direction}"] > before[f"n2_{direction}"], "active node was interrupted"
    assert forward_after[f"f1_{direction}"] == forward_before[f"f1_{direction}"], "retired forward counter changed"
    assert forward_after[f"f2_{direction}"] > forward_before[f"f2_{direction}"], "active forward was interrupted"
print("PASS retired NAT connections cannot communicate or change final node counters")
print("PASS retired forward counters freeze while another NAT forward continues")

client.terminate()
client.wait(timeout=2)
time.sleep(.2)
run("nft", "delete", "table", "inet", "ctlvps_nat")
apply("nodes-active.nft")
echo("192.0.1.3", 10001)
client = new_client()
before = snapshot()
for udp in (False, True):
    exchange(client, f"ordinary-{udp}", udp=udp, port=10001, prefix="192.0.1.3:10001|")
time.sleep(.2)
assert snapshot() == before, "deleted NAT table still captured new local-port traffic"
for udp in (False, True):
    exchange(client, f"logical-{udp}", udp=udp, port=30001)
assert snapshot()["n1_rx"] > before["n1_rx"], "normal node listener did not recover"
run("nft", "list", "table", "inet", "nat_reference")
print("PASS deleting only the NAT table restores ordinary local-port service and direct logical listeners")
