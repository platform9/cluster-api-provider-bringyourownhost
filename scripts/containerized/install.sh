#!/bin/bash
echo "set chroot to mount root on host "

chroot /host bash

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

./byoh-hostagent-linux-amd64 --bootstrap-kubeconfig bootstrap-kubeconfig.yaml  > ~/var/log/pf9/byoh/byoh-agent.log 2>&1 & disown -a

