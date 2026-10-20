"""Exercise unmodified production install-agent.sh with an offline HTTPS mirror.

Only the test container trusts the ephemeral CA and resolves GitHub to loopback.
APK dependencies are installed by the image build; no package repositories are
available during this network-none test. No service or agent command is mocked.
"""
import hashlib
import http.server
import json
import os
from pathlib import Path
import ssl
import subprocess
import threading
import time

assert os.environ.get("CTLVPS_ALPINE_FIXTURE") == "1"
assert Path("/.dockerenv").exists()
os.umask(0o022)
arch = os.environ["FIXTURE_ARCH"]
assert arch in ("amd64", "arm64")
root = Path("/run/installer-fixture")
root.mkdir(mode=0o700)
latest = "v0.0.0-fixture.1"
corrupt = False
enrollments = 0
heartbeats = 0


def run(*argv, ok=True):
    result = subprocess.run(argv, text=True, stdout=subprocess.PIPE,
                            stderr=subprocess.STDOUT, timeout=110)
    if ok and result.returncode:
        raise AssertionError(f"{argv[0]} failed: {result.stdout}")
    return result


class Mirror(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_args):
        pass

    def send(self, status, body=b""):
        self.send_response(status)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path == "/repos/YongshengWin/VpsCT/releases/latest":
            self.send(200, json.dumps({"tag_name": latest}).encode())
            return
        prefix = "/YongshengWin/VpsCT/releases/download/"
        parts = self.path.removeprefix(prefix).split("/")
        if self.path.startswith(prefix) and len(parts) == 2:
            version, name = parts
            if version not in ("v0.0.0-fixture.1", "v0.0.0-fixture.2"):
                self.send(404)
                return
            assets = Path("/fixtures") / version
            names = [f"ctlvps-agent-linux-{arch}", f"ctlvps-verify-linux-{arch}", "uninstall.sh"]
            if name == "SHA256SUMS":
                self.send(200, "".join(f"{hashlib.sha256((assets / n).read_bytes()).hexdigest()}  {n}\n"
                                       for n in names).encode())
                return
            if name in names:
                body = (assets / name).read_bytes()
                if corrupt and name.startswith("ctlvps-agent-"):
                    body += b"corrupt-download"
                self.send(200, body)
                return
        if self.path == "/api/agent/v1/desired" and self.headers.get("Authorization") == "Bearer fixture-only-agent":
            self.send(304)
            return
        self.send(404)

    def do_POST(self):
        global enrollments, heartbeats
        length = int(self.headers.get("Content-Length", "0"))
        assert length <= 8 << 20
        body = self.rfile.read(length)
        if self.path == "/api/agent/v1/enroll":
            request = json.loads(body)
            if request.get("enroll_token") != "fixture-only-enroll" or enrollments:
                self.send(401)
                return
            assert request["arch"] == arch and request["version"] == latest
            enrollments += 1
            self.send(200, json.dumps({"agent_token": "fixture-only-agent", "server_id": 71,
                                      "server_name": "installer-fixture", "poll_interval_sec": 10}).encode())
            return
        if self.path == "/api/agent/v1/heartbeat" and self.headers.get("Authorization") == "Bearer fixture-only-agent":
            heartbeats += 1
            self.send(200, b'{"poll_interval_sec":10}')
            return
        self.send(404)


assert "noexec" in run("findmnt", "-no", "OPTIONS", "/tmp").stdout.strip().split(",")
run("openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1",
    "-keyout", str(root / "key.pem"), "-out", str(root / "cert.pem"),
    "-subj", "/CN=VpsCT disposable installer fixture",
    "-addext", "subjectAltName=DNS:github.com,DNS:api.github.com,DNS:controller.test",
    "-addext", "basicConstraints=critical,CA:TRUE")
Path("/usr/local/share/ca-certificates/installer-fixture.crt").write_bytes((root / "cert.pem").read_bytes())
run("update-ca-certificates")
with Path("/etc/hosts").open("a") as hosts:
    hosts.write("\n127.0.0.1 github.com api.github.com controller.test\n")
# The image build performs the real apk download. Installed dependencies must
# satisfy the installer's actual apk invocation without accessing the network.
Path("/etc/apk/repositories").write_text("")
os.environ["NO_PROXY"] = "*"
os.environ["no_proxy"] = "*"
server = http.server.ThreadingHTTPServer(("127.0.0.1", 443), Mirror)
context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
context.load_cert_chain(root / "cert.pem", root / "key.pem")
server.socket = context.wrap_socket(server.socket, server_side=True)
threading.Thread(target=server.serve_forever, daemon=True).start()
installer = "/work/internal/assets/install-agent.sh"
agent = "/usr/local/bin/ctlvps-agent"
state_path = Path("/var/lib/ctlvps-agent/state.json")


def identity():
    state = json.loads(state_path.read_text())
    # Metering nonces can rotate on startup; enrollment identity must persist.
    return {key: state[key] for key in ("server_url", "agent_token", "server_id", "enrolled_at")}


def receipt():
    run(agent, "openrc-ready")
    return json.loads(Path("/run/ctlvps-openrc/agent-ready.json").read_text())


def wait_heartbeat(previous):
    deadline = time.monotonic() + 25
    while heartbeats <= previous:
        assert time.monotonic() < deadline, "installed service did not authenticate a heartbeat"
        time.sleep(0.1)


assert not state_path.exists() and not Path(agent).exists()
run("bash", installer, "--server", "https://controller.test", "--token", "fixture-only-enroll")
assert enrollments == 1
first_identity = identity()
assert first_identity["server_id"] == 71
assert json.loads(state_path.read_text())["openrc_version"] == 1
assert state_path.stat().st_mode & 0o777 == 0o600
assert Path("/etc/runlevels/default/ctlvps-agent").is_symlink()
first_receipt = receipt()
wait_heartbeat(0)
run("flock", "-n", "/run/lock/ctlvps-install.lock", "true")
before_hash = hashlib.sha256(Path(agent).read_bytes()).digest()
assert "v0.0.0-fixture.1" in run(agent, "version").stdout
print("PASS real Alpine first install, HTTPS checksum downloads, enrollment, autostart and authenticated heartbeat", flush=True)

latest = "v0.0.0-fixture.2"
corrupt = True
failed = run("bash", installer, "--update", "--server", "https://controller.test", ok=False)
assert failed.returncode and "agent SHA256 mismatch" in failed.stdout, failed.stdout
assert hashlib.sha256(Path(agent).read_bytes()).digest() == before_hash
assert receipt() == first_receipt and identity() == first_identity
print("PASS corrupt terminal update rejected without replacing the running agent or state", flush=True)

corrupt = False
previous_heartbeats = heartbeats
run("bash", installer, "--update", "--server", "https://controller.test")
assert "v0.0.0-fixture.2" in run(agent, "version").stdout
assert receipt()["pid"] != first_receipt["pid"]
assert identity() == first_identity and enrollments == 1
wait_heartbeat(previous_heartbeats)
assert hashlib.sha256(Path(agent).read_bytes()).digest() == hashlib.sha256(
    (Path("/fixtures") / latest / f"ctlvps-agent-linux-{arch}").read_bytes()).digest()
run("rc-service", "ctlvps-agent", "stop")
assert run(agent, "openrc-ready", ok=False).returncode
print("PASS real terminal update, new process readiness, unchanged enrollment identity and graceful stop", flush=True)
server.shutdown()
