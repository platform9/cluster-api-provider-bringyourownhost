FROM ubuntu:20.04

# Disable interactive prompts for APT
ENV DEBIAN_FRONTEND=noninteractive

# Install systemd and other required packages
RUN apt update && apt install -y \
    systemd systemd-sysv \
    wget curl iproute2 \
    socat ethtool ebtables conntrack \
    && apt clean && rm -rf /var/lib/apt/lists/*

# Enable systemd inside the container
VOLUME [ "/sys/fs/cgroup" ]
RUN systemctl mask serial-getty@ttyS0.service
# Copy agent package and install script
COPY pf9-byohost-agent.deb /
COPY install.sh install.sh

RUN chmod +x install.sh

# Start systemd as init system
CMD ["/sbin/init"]
