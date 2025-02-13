#!/bin/bash

echo "after pf9-byohost-agent installation"

mkdir -p /var/log/pf9/byoh
mkdir -p $HOME/.byoh/
touch  /var/log/pf9/byoh/byoh-agent-installation.log
apt install -y dependencies/socat.deb >> /var/log/pf9/byoh/byoh-agent-installation.log
apt install -y dependencies/ebtables.deb >> /var/log/pf9/byoh/byoh-agent-installation.log
apt install -y dependencies/ethtool.deb >> /var/log/pf9/byoh/byoh-agent-installation.log
apt install -y dependencies/conntrack.deb >> /var/log/pf9/byoh/byoh-agent-installation.log
dpkg -i ./pf9-byohost-agent.deb >> /var/log/pf9/byoh/byoh-agent-installation.log

