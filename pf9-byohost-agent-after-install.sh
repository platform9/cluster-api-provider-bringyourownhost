#!/bin/bash

echo "after pf9-byohost-agent installation"


mkdir -p /var/log/pf9/byoh
mkdir -p $HOME/.byoh/
touch  /var/log/pf9/byoh/byoh-agent.log
chmod +x /binary/pf9-byoh-hostagent-linux-amd64
cp /lib/systemd/system/pf9-byohost-agent.service  /etc/systemd/system/pf9-byohost-agent.service

if [ -z "$BOOTSTRAP_KUBECONFIG" ]; then
    echo "Error: BOOTSTRAP_KUBECONFIG environment variable is not set."
    exit 1
fi

if [ ! -f "$BOOTSTRAP_KUBECONFIG" ]; then
    echo "Error: File specified in "$BOOTSTRAP_KUBECONFIG" does not exist: $BOOTSTRAP_KUBECONFIG"
    exit 1
fi

mkdir -p /etc/pf9-byohost-agent.service.d/
touch /etc/pf9-byohost-agent.service.d/pf9-byohost-agent.conf
BT=$(echo $BOOTSTRAP_KUBECONFIG)
echo "BOOTSTRAP_KUBECONFIG="$BT > /etc/pf9-byohost-agent.service.d/pf9-byohost-agent.conf
systemctl daemon-reload
systemctl enable pf9-byohost-agent.service
systemctl start pf9-byohost-agent.service

