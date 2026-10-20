import http.server,ssl,threading,json
class H(http.server.BaseHTTPRequestHandler):
 def do_GET(self):
  body=json.dumps({'source':self.client_address[0],'path':self.path}).encode()
  self.send_response(200);self.send_header('Content-Length',str(len(body)));self.end_headers();self.wfile.write(body)
 def log_message(self,*a):pass
http_server=http.server.ThreadingHTTPServer(('0.0.0.0',8080),H)
threading.Thread(target=http_server.serve_forever,daemon=True).start()
ctx=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER);ctx.minimum_version=ssl.TLSVersion.TLSv1_3
ctx.load_cert_chain('/fixture/cert.pem','/fixture/key.pem')
ctx.set_alpn_protocols(['h2','http/1.1'])
s=http.server.ThreadingHTTPServer(('0.0.0.0',443),H);s.socket=ctx.wrap_socket(s.socket,server_side=True);s.serve_forever()
