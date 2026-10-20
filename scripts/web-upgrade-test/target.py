"""Synthetic HTTP and HTTPS target inside the offline target namespace."""
import ssl
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

class Target(BaseHTTPRequestHandler):
    def log_message(self, *_): pass
    def do_GET(self): self.send_response(204); self.end_headers()

plain = ThreadingHTTPServer(('93.184.216.35', 10080), Target)
threading.Thread(target=plain.serve_forever, daemon=True).start()
secure = ThreadingHTTPServer(('93.184.216.35', 10443), Target)
tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
tls.load_cert_chain('/usr/local/share/ca-certificates/web-upgrade-fixture.crt', '/tmp/fixture.key')
secure.socket = tls.wrap_socket(secure.socket, server_side=True)
secure.serve_forever()
