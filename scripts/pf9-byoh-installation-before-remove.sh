#!/bin/bash
sudo apt remove -y socat.deb
sudo apt remove -y ebtables.deb
sudo apt remove -y ethtool.deb
sudo apt remove -y conntrack.deb
dpkg --remove pf9-byohost-agent
dpkg --purge pf9-byohost-agent
