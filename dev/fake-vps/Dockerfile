# A stand-in for a customer's server: Ubuntu with systemd, sshd and Docker, so the
# bootstrap path exercised here is the same one that runs on a real machine. Without
# systemd the agent's service install could not be tested at all.
FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y --no-install-recommends \
	systemd systemd-sysv dbus dbus-user-session \
	openssh-server sudo ca-certificates curl gnupg \
	iproute2 iptables nftables procps \
	&& rm -rf /var/lib/apt/lists/*

# Docker from the official repository, matching what a real server would install.
RUN install -m 0755 -d /etc/apt/keyrings \
	&& curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc \
	&& chmod a+r /etc/apt/keyrings/docker.asc \
	&& echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu noble stable" \
		> /etc/apt/sources.list.d/docker.list \
	&& apt-get update \
	&& apt-get install -y --no-install-recommends \
		docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin \
	&& rm -rf /var/lib/apt/lists/*

# Development credentials only. This image is never published and never leaves the machine.
RUN mkdir -p /var/run/sshd /root/.ssh \
	&& chmod 700 /root/.ssh \
	&& echo 'root:yol-dev' | chpasswd \
	&& printf 'PermitRootLogin yes\nPasswordAuthentication yes\nUseDNS no\n' \
		> /etc/ssh/sshd_config.d/10-yol-dev.conf

COPY authorized_keys /root/.ssh/authorized_keys
RUN chmod 600 /root/.ssh/authorized_keys

RUN systemctl enable ssh docker

# Units that assume real hardware and only produce noise in a container.
RUN systemctl mask systemd-udevd systemd-udev-trigger systemd-modules-load \
	sys-kernel-debug.mount sys-kernel-tracing.mount

EXPOSE 22 80 443

STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
