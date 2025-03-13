#!/bin/bash
echo "setting up kernal modules inside container"
apt-get update && apt-get install -y linux-modules-$(uname -r)
echo " Starting pf9-byohost-agent"
hostnamectl set-hostname $HOSTNAME 
dpkg -i pf9-byohost-agent.deb
systemctl daemon-reload
systemctl enable pf9-byohost-agent.service
systemctl start pf9-byohost-agent.service
