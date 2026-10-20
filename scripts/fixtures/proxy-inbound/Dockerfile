FROM ghcr.io/sagernet/sing-box:v1.14.1
RUN apk add --no-cache python3 openssl && mkdir /fixture && openssl req -x509 -newkey rsa:2048 -nodes -keyout /fixture/key.pem -out /fixture/cert.pem -days 1 -subj /CN=handshake.example.test >/dev/null 2>&1
COPY server.py /fixture/server.py
ENTRYPOINT ["python3","/fixture/server.py"]
