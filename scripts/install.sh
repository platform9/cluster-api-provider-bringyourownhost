#!/bin/bash
echo "setting up kernal modules inside container"
echo "setting up kernel modules inside container"
	apt-get update
	KERNEL_VERSION=$(uname -r)
	if apt-cache search linux-modules-$KERNEL_VERSION | grep -q linux-modules-$KERNEL_VERSION; then
	  apt-get install -y linux-modules-$KERNEL_VERSION
	else
	  echo "Warning: linux-modules-$KERNEL_VERSION package not found. Continuing without installing kernel modules."
	fi

echo " Starting pf9-byohost-agent"
hostnamectl set-hostname $HOSTNAME
if ! dpkg -i pf9-byohost-agent.deb; then
	    echo "Failed to install pf9-byohost-agent package"
	    exit 1
	fi
