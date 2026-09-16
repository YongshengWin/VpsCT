FROM ubuntu:24.04
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
    ca-certificates curl tar coreutils util-linux iproute2 python3 openssl \
 && rm -rf /var/lib/apt/lists/*
COPY scripts/installer-test/systemctl.py /usr/local/bin/systemctl
RUN chmod 0755 /usr/local/bin/systemctl && mkdir -p /run/systemd/system
# systemctl is simulated; the real release binary handles health/setup requests.
