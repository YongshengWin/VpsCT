"""Container-only bootstrap proxy for the official ACME directory URL.

All ACME order/authorization URLs returned by Pebble retain its private address.
Only the production directory discovery URL is redirected, without modifying
production-generated sing-box configuration or adding a product override.
"""
import http.server
from pathlib import Path
import ssl
import sys
import urllib.request

root = Path(sys.argv[1])
context = ssl.create_default_context(cafile=str(root/'ca.crt'))


class Directory(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path != '/directory':
            self.send_error(404)
            return
        with urllib.request.urlopen('https://198.51.100.2:14000/dir', context=context, timeout=10) as response:
            body = response.read()
            self.send_response(200)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Content-Length', str(len(body)))
            self.end_headers()
            self.wfile.write(body)

    def log_message(self, *_):
        pass


server = http.server.HTTPServer(('198.51.100.2', 443), Directory)
server_context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
server_context.load_cert_chain(str(root/'ca.crt'), str(root/'ca.key'))
server.socket = server_context.wrap_socket(server.socket, server_side=True)
server.serve_forever()
