ARG BASE_IMAGE=ubuntu:20.04
FROM $BASE_IMAGE as build

RUN mkdir installer 
COPY install.sh installer/.
COPY socat.deb installer/.
COPY ethtool.deb installer/.
COPY ebtables.deb installer/.
COPY conntrack.deb installer/.
COPY pf9-byohost-agent.deb installer/.

WORKDIR installer
RUN chmod +x install.sh

USER 65532:65532
ENTRYPOINT ["/install.sh"]
