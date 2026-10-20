FROM alpine:3.23
RUN apk add --no-cache bash ca-certificates openrc nftables iproute2 \
    bubblewrap util-linux coreutils shadow chrony python3 procps libcap-utils \
    curl tcpdump iputils openssl unzip tar findmnt flock setpriv && \
    sed -i 's/^#rc_cgroup_mode=.*/rc_cgroup_mode="unified"/' /etc/rc.conf && \
    printf 'rc_provide="loopback net"\nrc_cgroup_mode="unified"\n' >> /etc/rc.conf && \
    mkdir -p /run/openrc && \
    printf '::sysinit:/sbin/openrc sysinit\n::wait:/sbin/openrc boot\n::wait:/sbin/openrc default\n::shutdown:/sbin/openrc shutdown\n' > /etc/inittab
CMD ["/sbin/init"]
