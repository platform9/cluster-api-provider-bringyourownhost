FROM ubuntu:22.04

# Disable interactive prompts for APT
ENV DEBIAN_FRONTEND=noninteractive

# Install required packages (no need for systemd in the container)
RUN apt update && apt install -y \
    wget curl iproute2 \
    socat ethtool ebtables conntrack \
    && apt clean && rm -rf /var/lib/apt/lists/*

# Copy the installation script
COPY install.sh /install.sh
RUN chmod +x /install.sh

# Set the entrypoint to run the installation script
ENTRYPOINT ["/install.sh"]
