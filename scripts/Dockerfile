FROM ubuntu:20.04


ENV DEBIAN_FRONTEND=noninteractive
USER root
COPY pf9-byohost-agent.deb /

RUN apt-get update && apt-get install -y \
    socat \
    ebtables \
    ethtool \
    conntrack \
    systemctl 
 
CMD dpkg -i /pf9-byohost-agent.deb
